package collector

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

type upstreamError struct{ retry time.Time }

func (e upstreamError) Error() string      { return "upstream unavailable" }
func (e upstreamError) Temporary() bool    { return true }
func (e upstreamError) RetryAt() time.Time { return e.retry }

func measured(at time.Time) domain.Snapshot {
	value := 21.0
	return domain.Snapshot{Provider: "fixture", CollectedAt: at, Homes: []domain.HomeReading{{ID: "1", Zones: []domain.ZoneReading{{TemperatureC: &value}}}}}
}

func TestHistoryFailureDoesNotRetryUpstreamOrInvalidateCache(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cache := &store.Store{}
	budget, _ := NewBudget(90, nil)
	reads, writes, observations := 0, 0, 0
	var log bytes.Buffer
	interval := time.Hour
	c, err := New(func(context.Context) (domain.Snapshot, int, error) {
		reads++
		if reads == 2 {
			return domain.Snapshot{}, 1, errors.New("upstream failed")
		}
		return measured(time.Now()), 1, nil
	}, cache, Options{
		Interval: interval, Budget: budget,
		Logger: slog.New(slog.NewTextHandler(&log, nil)),
		SaveHistory: func(_ context.Context, snapshot domain.Snapshot) error {
			writes++
			view, ok := cache.Load()
			if !ok || !view.CollectedAt.Equal(snapshot.CollectedAt) {
				t.Fatal("history ran before cache replacement")
			}
			return upstreamError{} // Even temporary database errors cannot retry tado.
		},
		ObserveHistory: func(err error) {
			if err == nil {
				t.Fatal("lost history error")
			}
			observations++
		},
		ObserveCollection: func(err error) {
			if reads == 1 && err != nil {
				t.Fatal("database error invalidated collection")
			}
		},
		Wait: func(ctx context.Context, delay time.Duration) error {
			if delay != interval {
				t.Fatalf("history error changed polling cadence: %v", delay)
			}
			if reads == 2 {
				cancel()
				return ctx.Err()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Run(ctx)
	if reads != 2 || writes != 1 || observations != 1 {
		t.Fatalf("reads=%d writes=%d observations=%d", reads, writes, observations)
	}
	if _, ok := cache.Load(); !ok {
		t.Fatal("history failure lost current data")
	}
	if !strings.Contains(log.String(), "history write failed") {
		t.Fatal("missing history failure log")
	}
}

func TestHistorySkipsInvalidSnapshotsAndSanitizesErrors(t *testing.T) {
	for _, valid := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		budget, _ := NewBudget(90, nil)
		var log bytes.Buffer
		writes := 0
		c, _ := New(func(context.Context) (domain.Snapshot, int, error) {
			if valid {
				return measured(time.Now()), 1, nil
			}
			return domain.Snapshot{}, 1, nil
		}, &store.Store{}, Options{Interval: time.Hour, Budget: budget,
			Logger: slog.New(slog.NewTextHandler(&log, nil)),
			SaveHistory: func(context.Context, domain.Snapshot) error {
				writes++
				return errors.New("postgres://PRIVATE:SECRET@host/db")
			},
			Wait: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() },
		})
		_ = c.Run(ctx)
		cancel()
		if (writes == 1) != valid || strings.Contains(log.String(), "SECRET") || strings.Contains(log.String(), "PRIVATE") {
			t.Fatal("invalid snapshot saved or driver error exposed")
		}
	}
}

func TestCollectorRetentionPacingAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	budget, _ := NewBudget(90, func() time.Time { return now })
	cache := &store.Store{}
	calls := 0
	var waits []time.Duration
	read := func(context.Context) (domain.Snapshot, int, error) {
		calls++
		if err := budget.Take(ctx); err != nil {
			t.Fatal(err)
		}
		if calls == 2 || calls == 3 {
			if _, ok := cache.Load(); !ok {
				t.Fatal("last good snapshot was lost")
			}
			next := time.Time{}
			if calls == 3 {
				next = now.Add(2 * time.Hour)
			}
			return domain.Snapshot{}, 4, upstreamError{retry: next}
		}
		return measured(now), 4, nil
	}
	poller, err := New(read, cache, Options{Interval: 30 * time.Minute, Budget: budget, Now: func() time.Time { return now }, Jitter: func(time.Duration) time.Duration { return 0 }, Wait: func(ctx context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		if len(waits) == 4 {
			cancel()
			return ctx.Err()
		}
		now = now.Add(delay)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := poller.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	expected := []time.Duration{64 * time.Minute, 30 * time.Second, 2 * time.Hour, 64 * time.Minute}
	if len(waits) != len(expected) {
		t.Fatalf("waits=%v", waits)
	}
	for i := range expected {
		if waits[i] != expected[i] {
			t.Fatalf("waits=%v", waits)
		}
	}
	view, ok := cache.Load()
	if !ok || !view.CollectedAt.Equal(now) || calls != 4 {
		t.Fatal("recovery did not replace the last snapshot")
	}
}

func TestCollectorRetriesAreBounded(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, _ := NewBudget(10000, func() time.Time { return now })
	var waits []time.Duration
	c, _ := New(func(context.Context) (domain.Snapshot, int, error) { return domain.Snapshot{}, 1, upstreamError{} }, &store.Store{}, Options{Interval: 10 * time.Minute, Budget: b, Now: func() time.Time { return now }, Jitter: func(time.Duration) time.Duration { return 0 }, Wait: func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		now = now.Add(d)
		if len(waits) == 4 {
			cancel()
			return ctx.Err()
		}
		return nil
	}})
	_ = c.Run(ctx)
	expected := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 10 * time.Minute}
	if len(waits) != 4 {
		t.Fatalf("waits=%v", waits)
	}
	for i := range expected {
		if waits[i] != expected[i] {
			t.Fatalf("waits=%v", waits)
		}
	}
}

func TestCollectorRejectsDuplicateRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	b, _ := NewBudget(90, nil)
	c, _ := New(func(ctx context.Context) (domain.Snapshot, int, error) {
		close(started)
		<-ctx.Done()
		return domain.Snapshot{}, 1, ctx.Err()
	}, &store.Store{}, Options{Interval: time.Minute, Budget: b})
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	<-started
	if err := c.Run(ctx); err == nil {
		t.Fatal("concurrent duplicate collection allowed")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCollectorWaitsForWholeCycleBudget(t *testing.T) {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, _ := NewBudget(4, func() time.Time { return now })
	count := 0
	c, _ := New(func(ctx context.Context) (domain.Snapshot, int, error) {
		count++
		if count > 1 {
			t.Fatal("started a cycle without reserving its request budget")
		}
		for range 3 {
			if err := b.Take(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return measured(now), 4, nil
	}, &store.Store{}, Options{Interval: time.Minute, Budget: b, Now: func() time.Time { return now }, Wait: func(ctx context.Context, d time.Duration) error {
		if d != 24*time.Hour {
			t.Fatalf("expected full rolling-window wait, got %v", d)
		}
		cancel()
		return ctx.Err()
	}})
	_ = c.Run(ctx)
}

func TestCollectionObserverIncludesCacheFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	budget, _ := NewBudget(90, nil)
	observed := false
	c, _ := New(func(context.Context) (domain.Snapshot, int, error) { return domain.Snapshot{}, 1, nil }, &store.Store{}, Options{
		Interval: time.Minute, Budget: budget,
		ObserveCollection: func(err error) { observed = err != nil },
		Wait:              func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() },
	})
	_ = c.Run(ctx)
	if !observed {
		t.Fatal("invalid cache replacement was counted as a successful collection")
	}
}
