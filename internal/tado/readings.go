package tado

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/domain"
)

// Read performs one collection. It never starts a poller or changes settings.
// A failed request aborts the collection; no partial snapshot is returned.
func (c *Client) Read(ctx context.Context, token credentials.Token) (domain.Snapshot, error) {
	homes, err := c.homes(ctx, token.AccessToken)
	if err != nil {
		return domain.Snapshot{}, err
	}
	result := domain.Snapshot{Provider: "tado", Homes: []domain.HomeReading{}}
	for _, home := range homes {
		generation, err := c.homeGeneration(ctx, token.AccessToken, home.ID)
		if err != nil {
			return domain.Snapshot{}, err
		}
		var zones []domain.ZoneReading
		if generation == "LINE_X" {
			zones, err = c.xReadings(ctx, token.AccessToken, home.ID)
		} else {
			zones, err = c.v3Readings(ctx, token.AccessToken, home.ID)
		}
		if err != nil {
			return domain.Snapshot{}, err
		}
		result.Homes = append(result.Homes, domain.HomeReading{ID: home.ID, Name: home.Name, Zones: zones})
	}
	result.CollectedAt = c.now().UTC()
	return result, nil
}

type temperature struct {
	Value     *float64   `json:"value"`
	Celsius   *float64   `json:"celsius"`
	Timestamp *time.Time `json:"timestamp"`
}

type sensors struct {
	InsideTemperature temperature `json:"insideTemperature"`
	Humidity          struct {
		Percentage *float64 `json:"percentage"`
	} `json:"humidity"`
}

type setting struct {
	Power       string      `json:"power"`
	Temperature temperature `json:"temperature"`
}

type heatingPower struct {
	Percentage *float64 `json:"percentage"`
}

func normalize(zone domain.Zone, sensor sensors, config setting, heating heatingPower, x bool) domain.ZoneReading {
	measured, target := sensor.InsideTemperature.Celsius, config.Temperature.Celsius
	if x {
		measured, target = sensor.InsideTemperature.Value, config.Temperature.Value
	}
	if config.Power == "OFF" {
		target = nil
	}
	var active *bool
	if heating.Percentage != nil && *heating.Percentage >= 0 && *heating.Percentage <= 100 {
		value := *heating.Percentage > 0
		active = &value
	}
	var measuredAt *time.Time
	if measured != nil && sensor.InsideTemperature.Timestamp != nil {
		utc := sensor.InsideTemperature.Timestamp.UTC()
		measuredAt = &utc
	}
	return domain.ZoneReading{Zone: zone, TemperatureC: measured, TargetTemperatureC: target, HumidityPercent: sensor.Humidity.Percentage, HeatingActive: active, TemperatureMeasuredAt: measuredAt}
}

func (c *Client) xReadings(ctx context.Context, bearer, homeID string) ([]domain.ZoneReading, error) {
	var rooms []struct {
		ID           json.Number  `json:"id"`
		Name         string       `json:"name"`
		Sensors      sensors      `json:"sensorDataPoints"`
		Setting      setting      `json:"setting"`
		HeatingPower heatingPower `json:"heatingPower"`
	}
	if err := c.request(ctx, http.MethodGet, c.xAPIURL+"/homes/"+url.PathEscape(homeID)+"/rooms", bearer, nil, &rooms); err != nil {
		return nil, err
	}
	if rooms == nil {
		return nil, errors.New("upstream X response has no rooms list")
	}
	result := []domain.ZoneReading{}
	for _, room := range rooms {
		if room.ID == "" {
			return nil, errors.New("upstream room has no ID")
		}
		result = append(result, normalize(domain.Zone{ID: string(room.ID), Name: room.Name}, room.Sensors, room.Setting, room.HeatingPower, true))
	}
	return result, nil
}

func (c *Client) v3Readings(ctx context.Context, bearer, homeID string) ([]domain.ZoneReading, error) {
	zones, err := c.v3Zones(ctx, bearer, homeID)
	if err != nil {
		return nil, err
	}
	result := []domain.ZoneReading{}
	for _, zone := range zones {
		var state *struct {
			Sensors  sensors `json:"sensorDataPoints"`
			Setting  setting `json:"setting"`
			Activity struct {
				HeatingPower heatingPower `json:"heatingPower"`
			} `json:"activityDataPoints"`
		}
		endpoint := c.apiURL + "/homes/" + url.PathEscape(homeID) + "/zones/" + url.PathEscape(zone.ID) + "/state"
		if err := c.request(ctx, http.MethodGet, endpoint, bearer, nil, &state); err != nil {
			return nil, err
		}
		if state == nil {
			return nil, errors.New("upstream response has no zone state")
		}
		result = append(result, normalize(zone, state.Sensors, state.Setting, state.Activity.HeatingPower, false))
	}
	return result, nil
}
