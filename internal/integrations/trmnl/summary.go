package trmnl

import (
	"context"
	"math"
	"time"

	"github.com/cpanato/tado-bridge/internal/history"
	"github.com/cpanato/tado-bridge/internal/store"
)

// Compact aliases-only projection; raw IDs and unselected rooms never leave the
// bridge. A missing summary is omitted, rather than replacing current readings.
type roomSummary struct {
	Date       string   `json:"date"`
	Min        *float64 `json:"min_c"`
	Max        *float64 `json:"max_c"`
	Mean       *float64 `json:"mean_c"`
	MeanChange *float64 `json:"mean_change_c"`
}

func rounded(value *float64) *float64 {
	if value == nil {
		return nil
	}
	result := math.Round(*value*10) / 10
	return &result
}

func (p *Publisher) payload(ctx context.Context, view store.View) ([]byte, bool, error) {
	if !p.config.SummaryEnabled || p.summaries == nil {
		return payload(view, p.config.Rooms, p.now(), p.maxAge)
	}
	location, err := time.LoadLocation(p.config.SummaryTimezone)
	if err != nil {
		return payload(view, p.config.Rooms, p.now(), p.maxAge)
	}
	at := view.CollectedAt.In(location)
	today := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, location)
	// Anchor summaries to the snapshot being published, so stale resends cannot
	// silently attach a different calendar day or change their historical window.
	result, err := p.summaries.Summarize(ctx, history.SummaryQuery{From: today.AddDate(0, 0, -1), To: view.CollectedAt.Add(time.Microsecond), Timezone: p.config.SummaryTimezone, MaxGap: history.DefaultSummaryGap})
	if err != nil {
		p.log("TRMNL historical summary unavailable; publishing current readings", 0)
		return payload(view, p.config.Rooms, p.now(), p.maxAge)
	}
	selected := map[[2]string]bool{}
	for _, room := range p.config.Rooms {
		selected[[2]string{room.HomeID, room.ZoneID}] = true
	}
	summaries := map[[2]string]*roomSummary{}
	for _, day := range result.Days {
		key := [2]string{day.HomeID, day.ZoneID}
		if selected[key] && day.Date == today.Format("2006-01-02") && day.TemperatureC.Samples > 0 {
			summaries[key] = &roomSummary{Date: day.Date, Min: rounded(day.TemperatureC.Min), Max: rounded(day.TemperatureC.Max), Mean: rounded(day.TemperatureC.Mean), MeanChange: rounded(day.TemperatureMeanChangeC)}
		}
	}
	data, stale, err := payloadWithSummary(view, p.config.Rooms, p.now(), p.maxAge, summaries, p.config.SummaryTimezone)
	if err != nil {
		p.log("TRMNL summary omitted to keep payload within size limit", 0)
		return payload(view, p.config.Rooms, p.now(), p.maxAge)
	}
	return data, stale, nil
}
