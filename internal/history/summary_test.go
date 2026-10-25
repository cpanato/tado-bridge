package history_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/history"
)

func summarySnapshot(at time.Time, temperature, target, humidity *float64, heating *bool) domain.Snapshot {
	return domain.Snapshot{Provider: "synthetic", CollectedAt: at, Homes: []domain.HomeReading{{ID: "h", Name: "Home", Zones: []domain.ZoneReading{{Zone: domain.Zone{ID: "z", Name: "Room"}, TemperatureC: temperature, TargetTemperatureC: target, HumidityPercent: humidity, HeatingActive: heating}}}}}
}

func TestDailySummarySamplesDurationsGapsAndComparison(t *testing.T) {
	value, _ := testDatabase(t)
	db := open(t, value)
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	zero, ten, twenty, fifty, eighty := 0.0, 10.0, 20.0, 50.0, 80.0
	on, off := true, false
	for _, s := range []domain.Snapshot{
		summarySnapshot(start, &ten, &twenty, &zero, &on),
		summarySnapshot(start.Add(time.Hour), &twenty, &twenty, nil, &off),
		summarySnapshot(start.Add(5*time.Hour), nil, nil, &eighty, nil),
		summarySnapshot(start.Add(23*time.Hour), &zero, &ten, &fifty, &on),
		summarySnapshot(start.Add(24*time.Hour+30*time.Minute), &twenty, nil, nil, &off),
	} {
		if err := db.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	query := history.SummaryQuery{From: start, To: start.Add(26 * time.Hour), Timezone: "UTC", MaxGap: 2 * time.Hour}
	result, err := db.Summarize(t.Context(), query)
	if err != nil || len(result.Days) != 2 {
		t.Fatalf("summary: %+v %v", result, err)
	}
	a, b := result.Days[0], result.Days[1]
	if a.TemperatureC.Samples != 3 || a.TemperatureC.Min == nil || *a.TemperatureC.Min != 0 || *a.TemperatureC.Max != 20 || *a.TemperatureC.Mean != 10 {
		t.Fatalf("sample stats: %+v", a.TemperatureC)
	}
	if a.BelowTargetSeconds != 7200 || a.TargetCoverageSeconds != 14400 || a.HeatingActiveSeconds != 7200 || a.HeatingCoverageSeconds != 14400 {
		t.Fatalf("unbounded gaps or incorrect durations: %+v", a)
	}
	if b.TemperatureC.Samples != 1 || *b.TemperatureC.Mean != 20 || b.BelowTargetSeconds != 1800 || b.TargetCoverageSeconds != 1800 || b.HeatingActiveSeconds != 1800 || b.HeatingCoverageSeconds != 7200 || b.TemperatureMeanChangeC == nil || *b.TemperatureMeanChangeC != 10 {
		t.Fatalf("boundary/comparison: %+v", b)
	}
	query.HomeID = "other"
	empty, err := db.Summarize(t.Context(), query)
	if err != nil || empty.Days == nil || len(empty.Days) != 0 {
		t.Fatal("empty room selection", err)
	}
	query.HomeID, query.ZoneID = "h", "z"
	if _, err := db.Summarize(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := db.Summarize(ctx, query); err == nil {
		t.Fatal("ignored cancellation")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Summarize(t.Context(), query); err == nil || err.Error() != "history summary query failed" {
		t.Fatal("unsanitized summary database error", err)
	}
}

func TestDailySummaryLocalDaysAndDST(t *testing.T) {
	for _, tc := range []struct {
		date  string
		hours int
	}{{"2026-03-29", 23}, {"2025-10-26", 25}} {
		t.Run(tc.date, func(t *testing.T) {
			value, _ := testDatabase(t)
			db, err := history.Open(t.Context(), value, 3650*24*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			loc, err := time.LoadLocation("Europe/Berlin")
			if err != nil {
				t.Fatal(err)
			}
			start, err := time.ParseInLocation("2006-01-02", tc.date, loc)
			if err != nil {
				t.Fatal(err)
			}
			end := start.AddDate(0, 0, 1)
			temperature, target := 10.0, 20.0
			on := true
			for at := start; at.Before(end); at = at.Add(time.Hour) {
				if err := db.Save(t.Context(), summarySnapshot(at, &temperature, &target, nil, &on)); err != nil {
					t.Fatal(err)
				}
			}
			result, err := db.Summarize(t.Context(), history.SummaryQuery{From: start, To: end, Timezone: "Europe/Berlin", MaxGap: time.Hour})
			if err != nil || len(result.Days) != 1 {
				t.Fatal("local day summary", err)
			}
			day := result.Days[0]
			if day.Date != tc.date || !day.DayStart.Equal(start) || day.TemperatureC.Samples != int64(tc.hours) || day.BelowTargetSeconds != float64(tc.hours*3600) {
				t.Fatalf("DST day: %+v", day)
			}
		})
	}
}

func TestDailySummaryMissingRoomEndsCoverage(t *testing.T) {
	value, _ := testDatabase(t)
	db := open(t, value)
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	temperature, target := 10.0, 20.0
	if err := db.Save(t.Context(), summarySnapshot(start, &temperature, &target, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(t.Context(), domain.Snapshot{Provider: "fixture", CollectedAt: start.Add(time.Hour), Homes: []domain.HomeReading{}}); err != nil {
		t.Fatal(err)
	}
	result, err := db.Summarize(t.Context(), history.SummaryQuery{From: start, To: start.Add(4 * time.Hour), Timezone: "UTC", MaxGap: 2 * time.Hour})
	if err != nil || len(result.Days) != 1 || result.Days[0].BelowTargetSeconds != 3600 {
		t.Fatalf("missing room extended coverage: %+v %v", result, err)
	}
}

func TestSummaryQueryBounds(t *testing.T) {
	now := time.Now().UTC()
	valid := history.SummaryQuery{From: now.Add(-time.Hour), To: now, Timezone: "UTC", MaxGap: time.Hour}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*history.SummaryQuery){
		func(q *history.SummaryQuery) { q.From = q.To },
		func(q *history.SummaryQuery) { q.From = q.To.Add(-32 * 24 * time.Hour) },
		func(q *history.SummaryQuery) { q.MaxGap = 0 },
		func(q *history.SummaryQuery) { q.MaxGap = 25 * time.Hour },
		func(q *history.SummaryQuery) { q.Timezone = "Local" },
		func(q *history.SummaryQuery) { q.Timezone = "Invalid/Zone" },
		func(q *history.SummaryQuery) { q.ZoneID = "missing-home" },
	} {
		q := valid
		change(&q)
		if q.Validate() == nil {
			t.Fatalf("invalid query accepted: %+v", q)
		}
	}
}

func TestSummaryExcludesExpiredCarryAndNonconsecutiveComparison(t *testing.T) {
	value, raw := testDatabase(t)
	db, err := history.Open(t.Context(), value, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	data, err := json.Marshal(summarySnapshot(now.Add(-25*time.Hour), ptr(10.0), ptr(20.0), nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `INSERT INTO tado_bridge_snapshots VALUES ($1,$2::jsonb)`, now.Add(-25*time.Hour), string(data)); err != nil {
		t.Fatal(err)
	}
	summary, err := db.Summarize(t.Context(), history.SummaryQuery{From: now.Add(-24 * time.Hour), To: now.Add(-23 * time.Hour), Timezone: "UTC", MaxGap: 24 * time.Hour})
	if err != nil || len(summary.Days) != 0 {
		t.Fatalf("expired sample carried into retained range: %+v %v", summary, err)
	}
	value, _ = testDatabase(t)
	other := open(t, value)
	start := now.Truncate(24 * time.Hour).Add(-4 * 24 * time.Hour)
	for _, at := range []time.Time{start, start.Add(3 * 24 * time.Hour)} {
		if err := other.Save(t.Context(), summarySnapshot(at, ptr(10.0), nil, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	summary, err = other.Summarize(t.Context(), history.SummaryQuery{From: start, To: now, Timezone: "UTC", MaxGap: time.Hour})
	if err != nil || len(summary.Days) != 2 || summary.Days[1].TemperatureMeanChangeC != nil {
		t.Fatalf("comparison crossed an unobserved day: %+v %v", summary, err)
	}
}

func ptr(value float64) *float64 { return &value }

func TestGrafanaDailyQueriesAgainstPostgres(t *testing.T) {
	value, raw := testDatabase(t)
	db := open(t, value)
	if err := db.Save(t.Context(), snapshot(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "integrations", "grafana", "tado-bridge-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []struct {
			Targets []struct {
				SQL string `json:"rawSql"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	replacer := strings.NewReplacer("$__timeFrom()", "(now() - interval '1 day')", "$__timeTo()", "(now() + interval '1 second')", "${summary_timezone:sqlstring}", "'UTC'", "${summary_max_gap:sqlstring}", "'7200'", "${summary_home:sqlstring}", "''", "${summary_room:sqlstring}", "''")
	count := 0
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			rows, err := raw.QueryContext(t.Context(), replacer.Replace(target.SQL))
			if err != nil {
				t.Fatal("summary SQL:", err)
			}
			if !rows.Next() {
				t.Fatal("summary panel returned no reading", rows.Err())
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			count++
		}
	}
	if count != 4 {
		t.Fatalf("expected four summary queries, got %d", count)
	}
}

func TestSummaryBoundsEscapedOutputAndRoomCount(t *testing.T) {
	value, _ := testDatabase(t)
	db := open(t, value)
	now := time.Now().UTC()
	start := now.Truncate(24 * time.Hour).Add(-3 * 24 * time.Hour)
	for i := range 3 {
		s := summarySnapshot(start.Add(time.Duration(i)*24*time.Hour), ptr(10), nil, nil, nil)
		s.Homes[0].Name = strings.Repeat("<", 500000)
		if err := db.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	query := history.SummaryQuery{From: start, To: now, Timezone: "UTC", MaxGap: time.Hour}
	if _, err := db.Summarize(t.Context(), query); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatal("escaped JSON response size was not bounded", err)
	}

	value, _ = testDatabase(t)
	db = open(t, value)
	at := now.Add(-time.Hour)
	s := summarySnapshot(at, nil, nil, nil, nil)
	s.Homes[0].Zones = nil
	for i := range 4100 {
		s.Homes[0].Zones = append(s.Homes[0].Zones, domain.ZoneReading{Zone: domain.Zone{ID: fmt.Sprint(i), Name: "Synthetic room"}, TemperatureC: ptr(10)})
	}
	if err := db.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	query.From = at.Add(-time.Hour)
	// Slow test hosts can reach the independent five-second deadline before
	// the 4,097th row. Both bounds must reject the query, never return a partial
	// success; selecting one room below must still work after cancellation.
	if _, err := db.Summarize(t.Context(), query); err == nil {
		t.Fatal("oversized room/day query returned a partial success")
	}
	query.HomeID, query.ZoneID = "h", "4099"
	selected, err := db.Summarize(t.Context(), query)
	if err != nil || len(selected.Days) != 1 || selected.Days[0].ZoneID != "4099" {
		t.Fatal("narrowed summary query lost a room", err)
	}
}
