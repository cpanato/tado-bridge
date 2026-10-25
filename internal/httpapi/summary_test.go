package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cpanato/tado-bridge/internal/history"
	"github.com/cpanato/tado-bridge/internal/store"
)

type summaryReader struct {
	historyReader
	summarize func(context.Context, history.SummaryQuery) (history.Summary, error)
}

func (s summaryReader) Summarize(ctx context.Context, q history.SummaryQuery) (history.Summary, error) {
	return s.summarize(ctx, q)
}

func TestSummaryAuthenticationValidationAndIsolation(t *testing.T) {
	const period = "from=2026-10-20T00:00:00Z&to=2026-10-22T00:00:00Z"
	calls := 0
	fail := false
	reader := summaryReader{summarize: func(ctx context.Context, q history.SummaryQuery) (history.Summary, error) {
		calls++
		if fail {
			return history.Summary{}, errors.New("postgres://PRIVATE:SECRET@host/db")
		}
		if ctx.Err() != nil {
			return history.Summary{}, ctx.Err()
		}
		if q.Timezone != "Europe/Berlin" || q.MaxGap.Hours() != 1 || q.HomeID != "h" || q.ZoneID != "z" {
			t.Fatalf("wrong query: %+v", q)
		}
		return history.Summary{Days: []history.DailySummary{}}, nil
	}}
	handler := New(&store.Store{}, Options{APIToken: "synthetic", History: reader})
	query := period + "&timezone=Europe%2FBerlin&max_gap=1h&home_id=h&zone_id=z"
	for _, tc := range []struct {
		method, query, auth string
		status              int
	}{
		{"GET", query, "", 401}, {"GET", query, "Bearer synthetic", 200}, {"POST", query, "Bearer synthetic", 405},
		{"GET", period + "&timezone=Local", "Bearer synthetic", 400}, {"GET", period + "&max_gap=0s", "Bearer synthetic", 400},
		{"GET", period + "&max_gap=bad", "Bearer synthetic", 400}, {"GET", period + "&zone_id=z", "Bearer synthetic", 400},
		{"GET", period + "&timezone=UTC&timezone=UTC", "Bearer synthetic", 400}, {"GET", period + "&limit=10", "Bearer synthetic", 400},
		{"GET", "from=invalid", "Bearer synthetic", 400},
	} {
		req := httptest.NewRequest(tc.method, "/api/v1/history/summary?"+tc.query, nil)
		req.Header.Set("Authorization", tc.auth)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", tc.query, response.Code, response.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("invalid request queried history: %d", calls)
	}
	fail = true
	req := httptest.NewRequest("GET", "/api/v1/history/summary?"+query, nil)
	req.Header.Set("Authorization", "Bearer synthetic")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 503 || strings.Contains(response.Body.String(), "PRIVATE") || strings.Contains(response.Body.String(), "SECRET") {
		t.Fatal("error exposed")
	}
	response = httptest.NewRecorder()
	New(&store.Store{}, Options{}).ServeHTTP(response, req)
	if response.Code != 404 {
		t.Fatal("summary must require history")
	}
}
