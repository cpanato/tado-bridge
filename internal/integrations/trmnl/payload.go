package trmnl

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

const maxPayloadBytes = 5_000

type roomReading struct {
	Name        string       `json:"name"`
	Available   bool         `json:"available"`
	Temperature *float64     `json:"temperature_c"`
	Target      *float64     `json:"target_temperature_c"`
	Humidity    *float64     `json:"humidity_percent"`
	Heating     *bool        `json:"heating_active"`
	Summary     *roomSummary `json:"summary,omitempty"`
}

type variables struct {
	MaxAgeSeconds   int64         `json:"max_age_seconds"`
	Source          string        `json:"source"`
	CollectedAt     time.Time     `json:"collected_at"`
	PublishedAt     time.Time     `json:"published_at"`
	Stale           bool          `json:"stale"`
	Rooms           []roomReading `json:"rooms"`
	SummaryTimezone string        `json:"summary_timezone,omitempty"`
}

func payload(view store.View, rooms []Room, now time.Time, maxAge time.Duration) ([]byte, bool, error) {
	return payloadWithSummary(view, rooms, now, maxAge, nil, "")
}

func payloadWithSummary(view store.View, rooms []Room, now time.Time, maxAge time.Duration, summaries map[[2]string]*roomSummary, timezone string) ([]byte, bool, error) {
	var snapshot domain.Snapshot
	if json.Unmarshal(view.JSON, &snapshot) != nil {
		return nil, false, errors.New("invalid cached snapshot")
	}
	stale := now.Sub(view.CollectedAt) > maxAge
	v := variables{MaxAgeSeconds: int64(maxAge / time.Second), Source: "tado-bridge", CollectedAt: view.CollectedAt.UTC(), PublishedAt: now.UTC(), Stale: stale}
	if len(summaries) > 0 {
		v.SummaryTimezone = timezone
	}
	for _, selection := range rooms {
		r := roomReading{Name: selection.Name}
		r.Summary = summaries[[2]string{selection.HomeID, selection.ZoneID}]
		for _, home := range snapshot.Homes {
			if home.ID != selection.HomeID {
				continue
			}
			for _, zone := range home.Zones {
				if zone.ID != selection.ZoneID {
					continue
				}
				r.Temperature, r.Target, r.Humidity, r.Heating = zone.TemperatureC, zone.TargetTemperatureC, zone.HumidityPercent, zone.HeatingActive
				r.Available = r.Temperature != nil || r.Target != nil || r.Humidity != nil || r.Heating != nil
			}
		}
		v.Rooms = append(v.Rooms, r)
	}
	data, err := json.Marshal(struct {
		Variables variables `json:"merge_variables"`
	}{v})
	if err != nil || len(data) > maxPayloadBytes {
		return nil, false, errors.New("TRMNL payload exceeds encoding or size limits")
	}
	return data, stale, nil
}
