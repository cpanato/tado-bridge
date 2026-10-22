package metrics

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

func TestMetricsFromCacheAndPrivacy(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cache := &store.Store{}
	exporter := New(cache, func() time.Time { return now }, Options{})
	scrape := func() string {
		t.Helper()
		response := httptest.NewRecorder()
		exporter.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
		if response.Code != 200 {
			t.Fatalf("metrics status=%d", response.Code)
		}
		return response.Body.String()
	}
	initial := scrape()
	if !strings.Contains(initial, "tado_bridge_collection_errors_total 0") || strings.Contains(initial, "tado_bridge_snapshot_age_seconds ") {
		t.Fatal("empty cache should not fabricate snapshot age")
	}
	low, high, humidity := 0.0, 24.0, 50.0
	at := now
	snapshot := domain.Snapshot{Provider: "private-provider", CollectedAt: at, Homes: []domain.HomeReading{{ID: "PRIVATE-HOME", Name: "PRIVATE-NAME", Zones: []domain.ZoneReading{{Zone: domain.Zone{ID: "PRIVATE-ID", Name: "PRIVATE-ROOM"}, TemperatureC: &low, HumidityPercent: &humidity}, {TemperatureC: &high}, {}}}}}
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	exporter.ObserveCollection(nil)
	exporter.ObserveCollection(errors.New("PRIVATE-ERROR"))
	exporter.ObserveRequests(3, 1)
	exporter.ObserveHistory(nil)
	exporter.ObserveHistory(errors.New("PRIVATE-ERROR"))
	now = now.Add(90 * time.Minute)
	result := scrape()
	for _, sample := range []string{
		"tado_bridge_snapshot_age_seconds 5400",
		`tado_bridge_temperature_celsius{statistic="min"} 0`,
		`tado_bridge_temperature_celsius{statistic="max"} 24`,
		`tado_bridge_temperature_celsius{statistic="mean"} 12`,
		`tado_bridge_humidity_percent{statistic="mean"} 50`,
		"tado_bridge_collection_errors_total 1",
		`tado_bridge_collections_total{result="success"} 1`,
		`tado_bridge_collections_total{result="error"} 1`,
		`tado_bridge_upstream_requests_total{kind="api"} 3`,
		`tado_bridge_upstream_requests_total{kind="oauth"} 1`,
		`tado_bridge_history_writes_total{result="success"} 1`,
		`tado_bridge_history_writes_total{result="error"} 1`,
	} {
		if !strings.Contains(result, sample) {
			t.Fatalf("missing sample: %s", sample)
		}
	}
	if strings.Contains(result, "PRIVATE") || strings.Contains(result, "private-provider") || strings.Contains(result, "tado_bridge_target_temperature_celsius{") {
		t.Fatal("metrics exposed identity/error text or fabricated missing target")
	}
	families, err := exporter.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() != "statistic" && label.GetName() != "kind" && label.GetName() != "result" && (label.GetName() != "room" || label.GetValue() != AllRooms) {
					t.Fatal("unexpected high-cardinality label")
				}
			}
		}
	}
}

func TestConcurrentMetrics(t *testing.T) {
	exporter := New(&store.Store{}, nil, Options{})
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 10 {
				exporter.ObserveCollection(nil)
				exporter.ObserveRequests(3, 1)
				if _, err := exporter.registry.Gather(); err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
}
