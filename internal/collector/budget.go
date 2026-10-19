package collector

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

const budgetWindow = 24 * time.Hour

type BudgetError struct{ until time.Time }

func (e *BudgetError) Error() string      { return "rolling 24-hour request budget exhausted" }
func (e *BudgetError) RetryAt() time.Time { return e.until }

// Budget counts attempted requests, including OAuth and failed network requests.
// State is process-local and resets on restart; upstream quotas remain authoritative.
type Budget struct {
	mu       sync.Mutex
	limit    int
	now      func() time.Time
	attempts []time.Time
}

func NewBudget(limit int, now func() time.Time) (*Budget, error) {
	if limit < 1 || limit > 1000000 {
		return nil, fmt.Errorf("request budget must be between 1 and 1000000")
	}
	if now == nil {
		now = time.Now
	}
	return &Budget{limit: limit, now: now}, nil
}

func (b *Budget) prune(now time.Time) {
	count := 0
	for count < len(b.attempts) && !b.attempts[count].Add(budgetWindow).After(now) {
		count++
	}
	b.attempts = append(b.attempts[:0], b.attempts[count:]...)
}

func (b *Budget) Take(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	if len(b.attempts) >= b.limit {
		return &BudgetError{until: b.attempts[0].Add(budgetWindow)}
	}
	b.attempts = append(b.attempts, now)
	return nil
}

// ReadyAt reports when an entire estimated cycle fits the remaining budget.
// Take still enforces the hard cap if discovery changes the actual cycle cost.
func (b *Budget) ReadyAt(cost int) (time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cost < 1 || cost > b.limit {
		return time.Time{}, fmt.Errorf("estimated cycle cost %d exceeds request budget %d", cost, b.limit)
	}
	now := b.now()
	b.prune(now)
	missing := len(b.attempts) + cost - b.limit
	if missing > 0 {
		return b.attempts[missing-1].Add(budgetWindow), nil
	}
	return now, nil
}

func (b *Budget) Pace(cost int) time.Duration {
	return time.Duration(math.Ceil(float64(budgetWindow) * float64(cost) / float64(b.limit)))
}
