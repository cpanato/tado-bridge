package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/history"
	"github.com/cpanato/tado-bridge/internal/store"
)

type historyReader func(context.Context, history.Query) (history.Page, error)

func (f historyReader) Query(ctx context.Context, query history.Query) (history.Page, error) {
	return f(ctx, query)
}

func TestHistoryAPIAuthenticationValidationAndAvailability(t *testing.T) {
	const period = "from=2026-10-20T00:00:00Z&to=2026-10-21T00:00:00Z"
	calls := 0
	unavailable := false
	reader := historyReader(func(ctx context.Context, query history.Query) (history.Page, error) {
		calls++
		if ctx.Err() != nil {
			return history.Page{}, ctx.Err()
		}
		if unavailable {
			return history.Page{}, errors.New("postgres://PRIVATE:SECRET@host/db")
		}
		if query.Limit != 2 || query.From.Day() != 20 || query.To.Day() != 21 {
			t.Fatalf("wrong query: %+v", query)
		}
		cursor := query.To.Add(-time.Hour)
		return history.Page{Snapshots: []json.RawMessage{json.RawMessage(`{"collected_at":"2026-10-20T23:00:00Z","homes":[]}`)}, NextTo: &cursor}, nil
	})
	handler := New(&store.Store{}, Options{APIToken: "test-token", History: reader})
	for _, tc := range []struct {
		method, query, auth string
		status              int
	}{
		{"GET", period + "&limit=2", "", 401},
		{"GET", period + "&limit=2", "Bearer test-token", 200},
		{"POST", period + "&limit=2", "Bearer test-token", 405},
		{"GET", "", "Bearer test-token", 400},
		{"GET", period + "&limit=0", "Bearer test-token", 400},
		{"GET", period + "&limit=101", "Bearer test-token", 400},
		{"GET", period + "&limit=bad", "Bearer test-token", 400},
		{"GET", period + "&limit=", "Bearer test-token", 400},
		{"GET", period + "&limit=2&limit=3", "Bearer test-token", 400},
		{"GET", period + "&home_id=private", "Bearer test-token", 400},
		{"GET", "from=2026-08-01T00:00:00Z&to=2026-10-21T00:00:00Z", "Bearer test-token", 400},
		{"GET", "from=2026-10-22T00:00:00Z&to=2026-10-21T00:00:00Z", "Bearer test-token", 400},
		{"GET", period + "&bad=%zz", "Bearer test-token", 400},
	} {
		t.Run(tc.method+tc.query+tc.auth, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1/history?"+tc.query, nil)
			req.Header.Set("Authorization", tc.auth)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("history may not be cached")
			}
			if tc.status == 200 {
				var page history.Page
				if json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Snapshots) != 1 || page.NextTo == nil {
					t.Fatal("invalid page response")
				}
			}
		})
	}
	if calls != 1 {
		t.Fatal("invalid or unauthenticated request queried the database")
	}
	unavailable = true
	response := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/history?"+period+"&limit=2", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	handler.ServeHTTP(response, req)
	if response.Code != 503 || strings.Contains(response.Body.String(), "SECRET") || strings.Contains(response.Body.String(), "PRIVATE") {
		t.Fatal("database error leaked or wrong status")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req.WithContext(ctx))
	if response.Code != 503 {
		t.Fatal("canceled request did not reach history context")
	}
	response = httptest.NewRecorder()
	New(&store.Store{}, Options{}).ServeHTTP(response, req)
	if response.Code != 404 {
		t.Fatal("history must be absent when disabled")
	}
}

func TestHistoryQueryDefaultsAndOffset(t *testing.T) {
	query, err := historyQuery(httptest.NewRequest("GET", "/api/v1/history?from=2026-10-20T02:00:00%2B02:00&to=2026-10-21T00:00:00Z", nil))
	if err != nil || query.Limit != 100 || !query.From.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("default limit or timezone handling", err)
	}
}
