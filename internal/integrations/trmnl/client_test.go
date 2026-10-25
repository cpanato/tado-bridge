package trmnl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/store"
)

func testPublisher(t *testing.T, cache *store.Store, handler http.HandlerFunc) *Publisher {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	p := New(Config{Enabled: true, WebhookURL: server.URL + "/secret-uuid", Interval: 30 * time.Minute, Rooms: []Room{{HomeID: "private-home", ZoneID: "private-zone", Name: "Room 1"}}}, cache, 2*time.Hour, nil, nil)
	p.client.Transport = server.Client().Transport
	p.jitter = func(time.Duration) time.Duration { return 0 }
	return p
}
func TestPublisherDeduplicatesAndMarksStale(t *testing.T) {
	now := time.Now().UTC()
	current := now
	cache := fixture(t, now)
	sends := 0
	p := testPublisher(t, cache, func(w http.ResponseWriter, r *http.Request) {
		sends++
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected request contract")
		}
		var data struct {
			Variables variables `json:"merge_variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		if data.Variables.Stale != (sends == 2) {
			t.Error("incorrect stale transition")
		}
		w.WriteHeader(200)
	})
	p.now = func() time.Time { return current }
	waits := 0
	p.wait = func(context.Context, time.Duration) error {
		waits++
		switch waits {
		case 1:
		case 2:
			current = now.Add(3 * time.Hour)
		case 3:
			return context.Canceled
		}
		return nil
	}
	if err := p.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if sends != 2 {
		t.Fatalf("sends=%d", sends)
	}
}
func TestPublisherDisabledAndEmptyCache(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		sends := 0
		p := testPublisher(t, &store.Store{}, func(http.ResponseWriter, *http.Request) { sends++ })
		p.config.Enabled = enabled
		p.wait = func(_ context.Context, d time.Duration) error {
			if d != 5*time.Second {
				t.Error("startup wait")
			}
			return context.Canceled
		}
		_ = p.Run(context.Background())
		if sends != 0 {
			t.Fatal("sent before opt-in or first collection")
		}
	}
}
func TestRetryPolicyAndSecretRedaction(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name   string
		status int
		retry  string
		delays []time.Duration
	}{
		{"backoff", 503, "", []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 30 * time.Minute}},
		{"rate-limit seconds", 429, "7200", []time.Duration{2 * time.Hour}},
		{"rate-limit date", 429, now.Add(3 * time.Hour).Format(http.TimeFormat), []time.Duration{3 * time.Hour}},
		{"rate-limit fallback", 429, "invalid", []time.Duration{time.Hour}},
		{"forbidden", 403, "", []time.Duration{30 * time.Minute}},
		{"redirect", 302, "", []time.Duration{30 * time.Minute}},
	} {
		t.Run(test.name, func(t *testing.T) {
			sends := 0
			logs := &bytes.Buffer{}
			p := testPublisher(t, fixture(t, now), func(w http.ResponseWriter, r *http.Request) {
				sends++
				if r.URL.Path != "/secret-uuid" {
					t.Error("followed redirect")
				}
				w.Header().Set("Retry-After", test.retry)
				w.Header().Set("Location", "/private-location")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte("private-secret-response"))
			})
			p.logger = slog.New(slog.NewTextHandler(logs, nil))
			p.now = func() time.Time { return now.Truncate(time.Second) }
			i := 0
			p.wait = func(_ context.Context, d time.Duration) error {
				if d != test.delays[i] {
					t.Errorf("delay=%s want=%s", d, test.delays[i])
				}
				i++
				if i == len(test.delays) {
					return context.Canceled
				}
				return nil
			}
			_ = p.Run(context.Background())
			if sends != len(test.delays) {
				t.Fatalf("sends=%d", sends)
			}
			for _, secret := range []string{"secret-uuid", "private-secret-response", "private-location", "private-home"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("secret leaked in log")
				}
			}
		})
	}
}
func TestPublisherTimeoutAndCancellation(t *testing.T) {
	now := time.Now()
	release := make(chan struct{})
	defer close(release)
	p := testPublisher(t, fixture(t, now), func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	p.client.Timeout = 100 * time.Millisecond
	waits := 0
	p.wait = func(context.Context, time.Duration) error { waits++; return context.Canceled }
	if err := p.Run(context.Background()); !errors.Is(err, context.Canceled) || waits != 1 {
		t.Fatalf("err=%v waits=%d", err, waits)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

func TestCancellationDuringSend(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	p := testPublisher(t, fixture(t, time.Now()), func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active request was not canceled")
	}
}
