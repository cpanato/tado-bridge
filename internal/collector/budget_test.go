package collector

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRollingBudget(t *testing.T) {
	now := time.Date(2026, 10, 19, 23, 59, 0, 0, time.UTC)
	b, err := NewBudget(2, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first := now
	if err := b.Take(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := b.Take(ctx); err != nil {
		t.Fatal(err)
	}
	var exhausted *BudgetError
	if err := b.Take(ctx); !errors.As(err, &exhausted) || !exhausted.RetryAt().Equal(first.Add(24*time.Hour)) {
		t.Fatalf("wrong reset: %v", err)
	}
	ready, err := b.ReadyAt(2)
	if err != nil || !ready.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("whole-cycle reservation: %v %v", ready, err)
	}
	now = first.Add(24 * time.Hour)
	if err := b.Take(ctx); err != nil {
		t.Fatal("first request should have aged out", err)
	}
	if err := b.Take(ctx); err == nil {
		t.Fatal("second request should still count")
	}
	if _, err := b.ReadyAt(3); err == nil {
		t.Fatal("accepted a cycle larger than the entire budget")
	}
}

func TestBudgetConcurrentAndCancellation(t *testing.T) {
	b, _ := NewBudget(10, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Take(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var success atomic.Int32
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if b.Take(context.Background()) == nil {
				success.Add(1)
			}
		})
	}
	group.Wait()
	if success.Load() != 10 {
		t.Fatalf("cap not enforced: %d", success.Load())
	}
}

func TestBudgetPacing(t *testing.T) {
	b, _ := NewBudget(90, nil)
	if got := b.Pace(4); got != 64*time.Minute {
		t.Fatalf("pace=%v", got)
	}
}
