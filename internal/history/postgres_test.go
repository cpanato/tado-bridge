package history_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/cpanato/tado-bridge/internal/collector"
	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/history"
	"github.com/cpanato/tado-bridge/internal/httpapi"
	"github.com/cpanato/tado-bridge/internal/metrics"
	"github.com/cpanato/tado-bridge/internal/store"
)

// Every integration test owns a schema in an explicitly selected test database.
// No real tado credentials or external APIs are used.
func testDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	value := os.Getenv("TEST_HISTORY_DATABASE_URL")
	if value == "" {
		t.Skip("set TEST_HISTORY_DATABASE_URL to run real PostgreSQL tests")
	}
	if err := history.ValidateURL(value); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(value)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	db := stdlib.OpenDB(*config)
	name := fmt.Sprintf("history_test_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(t.Context(), "CREATE SCHEMA "+name); err != nil {
		t.Fatal("create isolated test schema:", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := db.ExecContext(ctx, "DROP SCHEMA "+name+" CASCADE")
		if err != nil {
			t.Error("test schema cleanup failed")
		}
		_ = db.Close()
	})
	u, _ := url.Parse(value)
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	config, err = pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	scoped := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = scoped.Close() })
	return u.String(), scoped
}

func TestCollectedSnapshotServedFromCacheAndPostgres(t *testing.T) {
	value, _ := testDatabase(t)
	db := open(t, value)
	cache := &store.Store{}
	exporter := metrics.New(cache, nil, metrics.Options{})
	budget, _ := collector.NewBudget(90, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reads := 0
	poller, err := collector.New(func(context.Context) (domain.Snapshot, int, error) {
		reads++
		return snapshot(time.Now().UTC()), 4, nil
	}, cache, collector.Options{Interval: time.Hour, Budget: budget, SaveHistory: db.Save, ObserveHistory: exporter.ObserveHistory,
		Wait: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() },
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = poller.Run(ctx)
	handler := httpapi.New(cache, httpapi.Options{APIToken: "synthetic-token", MaxSnapshotAge: time.Hour, History: db, MetricsHandler: exporter.Handler()})
	query := url.Values{"from": {time.Now().Add(-time.Hour).Format(time.RFC3339Nano)}, "to": {time.Now().Add(time.Second).Format(time.RFC3339Nano)}}
	request := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer synthetic-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		return response
	}
	current := request("/api/v1/snapshot", 200)
	historical := request("/api/v1/history?"+query.Encode(), 200)
	var page history.Page
	if err := json.Unmarshal(historical.Body.Bytes(), &page); err != nil || len(page.Snapshots) != 1 {
		t.Fatal("collected snapshot missing from history", err)
	}
	var a, b domain.Snapshot
	if json.Unmarshal(current.Body.Bytes(), &a) != nil || json.Unmarshal(page.Snapshots[0], &b) != nil || !a.CollectedAt.Equal(b.CollectedAt) {
		t.Fatal("history changed collection time")
	}
	if !strings.Contains(request("/metrics", 200).Body.String(), `tado_bridge_history_writes_total{result="success"} 1`) {
		t.Fatal("successful persistence not observable")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	request("/api/v1/history?"+query.Encode(), 503)
	request("/api/v1/snapshot", 200)
	request("/healthz", 200)
	request("/readyz", 200)
	if reads != 1 {
		t.Fatal("history/API requests initiated additional upstream collection")
	}
}

func open(t *testing.T, value string) *history.Postgres {
	t.Helper()
	db, err := history.Open(t.Context(), value, history.DefaultRetention)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func snapshot(at time.Time) domain.Snapshot {
	zero, off := 0.0, false
	measured := at.Add(-5 * time.Minute)
	return domain.Snapshot{Provider: "fixture", CollectedAt: at, Homes: []domain.HomeReading{{ID: "home", Name: "Synthetic home", Zones: []domain.ZoneReading{{Zone: domain.Zone{ID: "room", Name: "Room 1"}, TemperatureC: &zero, HeatingActive: &off, TemperatureMeasuredAt: &measured}}}}}
}

func TestPostgresPersistencePaginationRetentionAndNulls(t *testing.T) {
	value, raw := testDatabase(t)
	db := open(t, value)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, at := range []time.Time{now.Add(-100 * 24 * time.Hour), now.Add(-2 * time.Hour), now.Add(-time.Hour), now} {
		if err := db.Save(t.Context(), snapshot(at)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Save(t.Context(), snapshot(now)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := raw.QueryRowContext(t.Context(), "SELECT count(*) FROM tado_bridge_snapshots").Scan(&count); err != nil || count != 3 {
		t.Fatalf("retention/idempotency: rows=%d err=%v", count, err)
	}
	query := history.Query{From: now.Add(-24 * time.Hour), To: now.Add(time.Second), Limit: 2}
	page, err := db.Query(t.Context(), query)
	if err != nil || len(page.Snapshots) != 2 || page.NextTo == nil || !page.NextTo.Equal(now.Add(-time.Hour)) {
		t.Fatalf("first page: %+v err=%v", page, err)
	}
	var decoded domain.Snapshot
	if err := json.Unmarshal(page.Snapshots[0], &decoded); err != nil {
		t.Fatal(err)
	}
	zone := decoded.Homes[0].Zones[0]
	if zone.TemperatureC == nil || *zone.TemperatureC != 0 || zone.HeatingActive == nil || *zone.HeatingActive || zone.HumidityPercent != nil || zone.TargetTemperatureC != nil || zone.TemperatureMeasuredAt == nil || !zone.TemperatureMeasuredAt.Equal(now.Add(-5*time.Minute)) {
		t.Fatal("null, zero, false or sensor timestamp changed")
	}
	query.To = *page.NextTo
	page, err = db.Query(t.Context(), query)
	if err != nil || len(page.Snapshots) != 1 || page.NextTo != nil {
		t.Fatalf("next page: %+v err=%v", page, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := open(t, value)
	query.Limit, query.To = 100, now.Add(time.Second)
	page, err = reopened.Query(t.Context(), query)
	if err != nil || len(page.Snapshots) != 3 {
		t.Fatal("history did not survive reconnection")
	}
	query.From, query.To = now.Add(time.Hour), now.Add(2*time.Hour)
	page, err = reopened.Query(t.Context(), query)
	if err != nil || page.Snapshots == nil || len(page.Snapshots) != 0 {
		t.Fatal("empty history must be []")
	}
	// Reads exclude expired data even before the next collection prunes storage.
	if _, err := raw.ExecContext(t.Context(), "INSERT INTO tado_bridge_snapshots VALUES ($1, '{}'::jsonb)", now.Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	query.From, query.To = now.Add(-110*24*time.Hour), now.Add(-95*24*time.Hour)
	page, err = reopened.Query(t.Context(), query)
	if err != nil || len(page.Snapshots) != 0 {
		t.Fatal("expired history returned")
	}
}

func TestPostgresBoundsCancellationAndRedaction(t *testing.T) {
	value, _ := testDatabase(t)
	db := open(t, value)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := range 3 {
		s := snapshot(now.Add(-time.Duration(i) * time.Second))
		s.Homes[0].Name = strings.Repeat("x", 3<<20)
		if err := db.Save(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	query := history.Query{From: now.Add(-time.Hour), To: now.Add(time.Second), Limit: 100}
	page, err := db.Query(t.Context(), query)
	if err != nil || len(page.Snapshots) != 2 || page.NextTo == nil {
		t.Fatal("page byte bound not enforced", err)
	}
	query.To = *page.NextTo
	page, err = db.Query(t.Context(), query)
	if err != nil || len(page.Snapshots) != 1 || page.NextTo != nil {
		t.Fatal("byte-limited pagination lost a snapshot", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := db.Query(ctx, query); err == nil {
		t.Fatal("query ignored cancellation")
	}
	if err := db.Save(ctx, snapshot(now)); err == nil {
		t.Fatal("write ignored cancellation")
	}
	if err := db.Save(t.Context(), domain.Snapshot{}); err == nil {
		t.Fatal("invalid snapshot persisted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(t.Context(), snapshot(now)); err == nil || err.Error() != "history write failed" {
		t.Fatal("driver write error was not sanitized")
	}
	if _, err := db.Query(t.Context(), query); err == nil || err.Error() != "history query failed" {
		t.Fatal("driver query error was not sanitized")
	}
}

func TestPostgresConcurrentInitialization(t *testing.T) {
	value, _ := testDatabase(t)
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			db, err := history.Open(t.Context(), value, history.DefaultRetention)
			if err != nil {
				t.Error(err)
				return
			}
			_ = db.Close()
		})
	}
	group.Wait()
}

func TestPostgresWriteAndRetentionAreAtomicAndRecover(t *testing.T) {
	value, raw := testDatabase(t)
	db := open(t, value)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := raw.ExecContext(t.Context(), `INSERT INTO tado_bridge_snapshots VALUES ($1, '{}'::jsonb)`, now.Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `CREATE FUNCTION reject_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'PRIVATE_SECRET'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), `CREATE TRIGGER deny_cleanup BEFORE DELETE ON tado_bridge_snapshots FOR EACH ROW EXECUTE FUNCTION reject_delete()`); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(t.Context(), snapshot(now)); err == nil || strings.Contains(err.Error(), "PRIVATE_SECRET") {
		t.Fatal("cleanup failure ignored or exposed")
	}
	var count int
	if err := raw.QueryRowContext(t.Context(), `SELECT count(*) FROM tado_bridge_snapshots WHERE collected_at = $1`, now).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed transaction partially persisted data", err)
	}
	if _, err := raw.ExecContext(t.Context(), `DROP TRIGGER deny_cleanup ON tado_bridge_snapshots`); err != nil {
		t.Fatal(err)
	}
	if err := db.Save(t.Context(), snapshot(now)); err != nil {
		t.Fatal("write did not recover", err)
	}
	if err := raw.QueryRowContext(t.Context(), `SELECT count(*) FROM tado_bridge_snapshots`).Scan(&count); err != nil || count != 1 {
		t.Fatal("recovery failed to save/prune", err)
	}
}

func TestGrafanaHistoricalQueriesAgainstPostgres(t *testing.T) {
	value, raw := testDatabase(t)
	db := open(t, value)
	if err := db.Save(t.Context(), snapshot(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "integrations", "grafana", "tado-bridge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []struct {
			Targets []struct {
				RawSQL string `json:"rawSql"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	queries := 0
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			if target.RawSQL == "" {
				continue
			}
			queries++
			query := strings.ReplaceAll(target.RawSQL, "$__timeFilter(s.collected_at)", "s.collected_at BETWEEN now() - interval '1 day' AND now() + interval '1 second'")
			query = strings.ReplaceAll(query, "$__timeGroupAlias(s.collected_at, $__interval)", "floor(extract(epoch from s.collected_at)/60)*60 AS time")
			rows, err := raw.QueryContext(t.Context(), query)
			if err != nil {
				t.Fatal("Grafana SQL:", err)
			}
			if !rows.Next() {
				t.Fatal("dashboard did not return the synthetic reading", rows.Err())
			}
			var at float64
			var metric string
			var reading *float64
			if err := rows.Scan(&at, &metric, &reading); err != nil {
				t.Fatal(err)
			}
			if at <= 0 || metric == "" {
				t.Fatal("invalid Grafana time series")
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if queries != 4 {
		t.Fatalf("expected four historical queries, got %d", queries)
	}
}
