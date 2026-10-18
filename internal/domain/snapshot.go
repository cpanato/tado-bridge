package domain

import "time"

// Snapshot is a completed point-in-time collection, not a sensor freshness claim.
type Snapshot struct {
	Provider    string        `json:"provider"`
	CollectedAt time.Time     `json:"collected_at"`
	Homes       []HomeReading `json:"homes"`
}

type HomeReading struct {
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Zones []ZoneReading `json:"zones"`
}

type ZoneReading struct {
	Zone
	TemperatureC          *float64   `json:"temperature_c"`
	TargetTemperatureC    *float64   `json:"target_temperature_c"`
	HumidityPercent       *float64   `json:"humidity_percent"`
	HeatingActive         *bool      `json:"heating_active"`
	TemperatureMeasuredAt *time.Time `json:"temperature_measured_at"`
}
