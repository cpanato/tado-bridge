package history

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"
	_ "time/tzdata" // Static containers also need IANA zones for local-day summaries.
)

//go:embed summary.sql
var summarySchema string

const DefaultSummaryGap = 2 * time.Hour

// SummaryQuery selects local calendar days intersecting [From, To). MaxGap
// bounds estimated holding time; averages/minima/maxima use actual samples only.
type SummaryQuery struct {
	From     time.Time
	To       time.Time
	Timezone string
	MaxGap   time.Duration
	HomeID   string
	ZoneID   string
}

func (q SummaryQuery) Validate() error {
	if err := (Query{From: q.From, To: q.To, Limit: 1}).Validate(); err != nil {
		return err
	}
	if q.MaxGap < time.Minute || q.MaxGap > 24*time.Hour {
		return errors.New("summary max_gap must be between 1m and 24h")
	}
	if len(q.HomeID) > 128 || len(q.ZoneID) > 128 || (q.ZoneID != "" && q.HomeID == "") {
		return errors.New("summary zone_id requires home_id; identifiers must be at most 128 bytes")
	}
	if q.Timezone == "" || q.Timezone == "Local" || len(q.Timezone) > 128 {
		return errors.New("summary timezone must be an IANA timezone")
	}
	if _, err := time.LoadLocation(q.Timezone); err != nil {
		return errors.New("summary timezone must be an IANA timezone")
	}
	return nil
}

type Statistics struct {
	Min     *float64 `json:"min"`
	Max     *float64 `json:"max"`
	Mean    *float64 `json:"mean"`
	Samples int64    `json:"samples"`
}

type DailySummary struct {
	Date                   string     `json:"date"`
	DayStart               time.Time  `json:"day_start"`
	HomeID                 string     `json:"home_id"`
	ZoneID                 string     `json:"zone_id"`
	HomeName               string     `json:"home_name"`
	ZoneName               string     `json:"zone_name"`
	TemperatureC           Statistics `json:"temperature_c"`
	TargetTemperatureC     Statistics `json:"target_temperature_c"`
	HumidityPercent        Statistics `json:"humidity_percent"`
	BelowTargetSeconds     float64    `json:"below_target_seconds"`
	TargetCoverageSeconds  float64    `json:"target_coverage_seconds"`
	HeatingActiveSeconds   float64    `json:"heating_active_seconds"`
	HeatingCoverageSeconds float64    `json:"heating_coverage_seconds"`
	TemperatureMeanChangeC *float64   `json:"temperature_mean_change_c"`
}

type Summary struct {
	From          time.Time      `json:"from"`
	To            time.Time      `json:"to"`
	Timezone      string         `json:"timezone"`
	MaxGapSeconds float64        `json:"max_gap_seconds"`
	Days          []DailySummary `json:"days"`
}

type Summarizer interface {
	Summarize(context.Context, SummaryQuery) (Summary, error)
}

func (p *Postgres) Summarize(ctx context.Context, query SummaryQuery) (Summary, error) {
	if err := query.Validate(); err != nil {
		return Summary{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Never use a pre-retention sample as a carried value at the cutoff.
	cutoff := p.now().Add(-p.retention)
	from := maxTime(query.From, cutoff)
	rows, err := p.db.QueryContext(ctx, `SELECT value FROM tado_bridge_daily_summary($1,$2,$3,$4,$5,$6,$7) AS value`, from, query.To, query.Timezone, query.MaxGap.Seconds(), query.HomeID, query.ZoneID, cutoff)
	if err != nil {
		return Summary{}, errors.New("history summary query failed")
	}
	defer func() { _ = rows.Close() }()
	result := Summary{From: query.From.UTC(), To: query.To.UTC(), Timezone: query.Timezone, MaxGapSeconds: query.MaxGap.Seconds(), Days: []DailySummary{}}
	size := 0
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return Summary{}, errors.New("history summary query failed")
		}
		if len(result.Days) >= 4096 || len(data) > maxPageBytes {
			return Summary{}, errors.New("history summary too large; select fewer days or a home/room")
		}
		var day DailySummary
		if json.Unmarshal(data, &day) != nil {
			return Summary{}, errors.New("invalid stored history summary")
		}
		// Account for JSON escaping in the actual HTTP representation, array
		// separators and room for the bounded response metadata.
		encoded, err := json.Marshal(day)
		if err != nil {
			return Summary{}, errors.New("invalid stored history summary")
		}
		size += len(encoded) + 1
		if size > maxPageBytes-1024 {
			return Summary{}, errors.New("history summary too large; select fewer days or a home/room")
		}
		result.Days = append(result.Days, day)
	}
	if rows.Err() != nil {
		return Summary{}, errors.New("history summary query failed")
	}
	return result, nil
}
