package tado

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/collector"
	"github.com/cpanato/tado-bridge/internal/credentials"
)

func TestRetryDeadline(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		after, limit string
		delay        time.Duration
	}{
		{"120", "", 2 * time.Minute},
		{now.Add(time.Hour).Format(http.TimeFormat), "", time.Hour},
		{"garbage", "", 0},
		{"", `"perday";r=0;t=300`, 5 * time.Minute},
		{"120", `"perday";r=0;t=300`, 5 * time.Minute},
		{"", `"perday";r=3;t=300`, 0},
	} {
		header := http.Header{}
		header.Set("Retry-After", tc.after)
		header.Set("RateLimit", tc.limit)
		got := retryDeadline(header, now)
		if tc.delay == 0 {
			if !got.IsZero() {
				t.Fatalf("unexpected deadline: %v", got)
			}
		} else if !got.Equal(now.Add(tc.delay)) {
			t.Fatalf("deadline=%v", got)
		}
	}
}

func Test429StopsRequestsUntilReset(t *testing.T) {
	now := time.Now()
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"error":"private-secret"}`)
			return
		}
		_, _ = io.WriteString(w, `{"homes":[]}`)
	})
	c.now = func() time.Time { return now }
	for range 2 {
		_, err := c.Discover(context.Background(), credentials.Token{})
		var deferred *responseError
		if !errors.As(err, &deferred) || !deferred.Temporary() || !deferred.RetryAt().Equal(now.Add(2*time.Minute)) {
			t.Fatalf("err=%v", err)
		}
	}
	if calls != 1 || c.APIRequests != 1 {
		t.Fatal("made another request during upstream cooldown")
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.Discover(context.Background(), credentials.Token{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("did not resume at reset")
	}
}

func TestGuardCountsOAuthAndREST(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/token" {
			_, _ = io.WriteString(w, `{"access_token":"fixture","refresh_token":"fixture","expires_in":600,"token_type":"Bearer"}`)
			return
		}
		_, _ = io.WriteString(w, `{"homes":[]}`)
	})
	budget, _ := collector.NewBudget(2, nil)
	c.SetRequestGuard(budget.Take)
	token, err := c.Refresh(context.Background(), credentials.Token{RefreshToken: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Discover(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Refresh(context.Background(), token); err == nil {
		t.Fatal("refresh exceeded budget")
	}
	if calls != 2 || c.OAuthRequests != 1 || c.APIRequests != 1 {
		t.Fatal("blocked request reached upstream or request accounting is incorrect")
	}
}
