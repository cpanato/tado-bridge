package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

func TestCachedAPIAndReadiness(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cache := &store.Store{}
	handler := New(cache, Options{MaxSnapshotAge: time.Hour, Now: func() time.Time { return now }})
	request := func(path string, status int, code string) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != status || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status=%d headers=%v", path, response.Code, response.Header())
		}
		if code != "" {
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Error.Code != code {
				t.Fatalf("wrong error: %s", response.Body.String())
			}
		}
	}
	request("/healthz", 200, "")
	request("/readyz", 503, "snapshot_unavailable")
	request("/api/v1/snapshot", 503, "snapshot_unavailable")
	value := 0.0
	snapshot := domain.Snapshot{Provider: "fixture", CollectedAt: now, Homes: []domain.HomeReading{{ID: "1", Zones: []domain.ZoneReading{{TemperatureC: &value}}}}}
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	for range 25 {
		request("/api/v1/snapshot", 200, "")
	}
	request("/readyz", 200, "")
	now = now.Add(time.Hour)
	request("/readyz", 200, "")
	now = now.Add(time.Nanosecond)
	request("/readyz", 503, "snapshot_stale")
	request("/api/v1/snapshot", 200, "")
	request("/healthz", 200, "")
	snapshot.CollectedAt = now
	snapshot.Homes = []domain.HomeReading{}
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	request("/readyz", 503, "snapshot_unusable")
	request("/metrics", 404, "not_found")
}

func TestBearerAndMethods(t *testing.T) {
	cache := &store.Store{}
	now := time.Now()
	active := false
	if err := cache.Save(domain.Snapshot{Provider: "fixture", CollectedAt: now, Homes: []domain.HomeReading{{Zones: []domain.ZoneReading{{HeatingActive: &active}}}}}); err != nil {
		t.Fatal(err)
	}
	handler := New(cache, Options{MaxSnapshotAge: time.Hour, APIToken: "test-secret", Now: func() time.Time { return now }})
	for _, tc := range []struct {
		path, method, auth string
		status             int
	}{
		{"/api/v1/snapshot", "GET", "", 401},
		{"/api/v1/snapshot", "GET", "Basic test-secret", 401},
		{"/api/v1/snapshot", "GET", "Bearer incorrect", 401},
		{"/api/v1/snapshot", "GET", "Bearer test-secret", 200},
		{"/api/v1/snapshot", "GET", "bearer test-secret", 200},
		{"/api/v1/snapshot", "POST", "Bearer test-secret", 405},
		{"/healthz", "GET", "", 200},
		{"/readyz", "GET", "", 200},
		{"/readyz", "POST", "", 405},
	} {
		t.Run(tc.path+tc.method+tc.auth, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d expected=%d", response.Code, tc.status)
			}
			if strings.Contains(response.Body.String(), "test-secret") {
				t.Fatal("token leaked into response")
			}
			if tc.status == 401 && response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing challenge")
			}
			if tc.status == 405 && response.Header().Get("Allow") != "GET" {
				t.Fatal("missing Allow header")
			}
		})
	}
	req := httptest.NewRequest("GET", "/api/v1/snapshot", nil)
	req.Header.Add("Authorization", "Bearer test-secret")
	req.Header.Add("Authorization", "Bearer test-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("accepted ambiguous authorization")
	}
}

func TestMetricsAreOptionalAndAuthenticated(t *testing.T) {
	calls := 0
	metrics := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("test_metric 1\n"))
	})
	for _, enabled := range []bool{false, true} {
		options := Options{MaxSnapshotAge: time.Hour, APIToken: "test-secret"}
		if enabled {
			options.MetricsHandler = metrics
		}
		handler := New(&store.Store{}, options)
		for _, auth := range []bool{false, true} {
			req := httptest.NewRequest("GET", "/metrics", nil)
			if auth {
				req.Header.Set("Authorization", "Bearer test-secret")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			expected := 404
			if enabled {
				expected = 401
				if auth {
					expected = 200
				}
			}
			if response.Code != expected {
				t.Fatalf("enabled=%v auth=%v status=%d", enabled, auth, response.Code)
			}
		}
	}
	if calls != 1 {
		t.Fatal("disabled or unauthenticated scrape reached exporter")
	}
}
