// Package collector serializes scheduled collection and preserves the last good snapshot.
package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

// Read returns a complete snapshot and an estimated cycle cost, including OAuth reserve.
type Read func(context.Context) (domain.Snapshot, int, error)

type Options struct {
	Interval          time.Duration
	Budget            *Budget
	Now               func() time.Time
	Wait              func(context.Context, time.Duration) error
	Jitter            func(time.Duration) time.Duration
	Logger            *slog.Logger
	ObserveCollection func(error)
	SaveHistory       func(context.Context, domain.Snapshot) error
	ObserveHistory    func(error)
}

type Collector struct {
	read    Read
	store   *store.Store
	options Options
	running atomic.Bool
}

func New(read Read, cache *store.Store, options Options) (*Collector, error) {
	if read == nil || cache == nil || options.Budget == nil || options.Interval <= 0 {
		return nil, errors.New("invalid collector configuration")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Wait == nil {
		options.Wait = wait
	}
	if options.Jitter == nil {
		options.Jitter = func(base time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(base/4) + 1)) }
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Collector{read: read, store: cache, options: options}, nil
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Collector) Run(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return errors.New("collector already running")
	}
	defer c.running.Store(false)
	expected, retries := 1, 0
	delay := time.Duration(0)
	for {
		ready, err := c.options.Budget.ReadyAt(expected)
		if err != nil {
			return err
		}
		if remaining := ready.Sub(c.options.Now()); remaining > delay {
			delay = remaining
		}
		if delay > 0 {
			if err := c.options.Wait(ctx, delay); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, cost, err := c.read(ctx)
		if cost > expected {
			expected = cost
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			err = c.store.Save(snapshot)
		}
		if c.options.ObserveCollection != nil {
			c.options.ObserveCollection(err)
		}
		if err == nil && c.options.SaveHistory != nil {
			historyErr := c.options.SaveHistory(ctx, snapshot)
			if c.options.ObserveHistory != nil {
				c.options.ObserveHistory(historyErr)
			}
			if historyErr != nil {
				// Do not log driver errors: they may contain database credentials.
				c.options.Logger.Error("history write failed; current snapshot remains available")
			}
		}
		cadence := max(c.options.Interval, c.options.Budget.Pace(expected))
		delay = cadence
		if err == nil {
			retries = 0
			c.options.Logger.Info("collection completed", "estimated_requests", expected, "next_collection_in", cadence)
		} else {
			c.options.Logger.Error("collection failed; keeping last snapshot", "error", err)
			var transient interface{ Temporary() bool }
			if errors.As(err, &transient) && transient.Temporary() && retries < 3 {
				backoff := min(30*time.Second*time.Duration(1<<retries), 15*time.Minute)
				delay = backoff + c.options.Jitter(backoff)
				retries++
			} else {
				retries = 0
			}
			var deferred interface{ RetryAt() time.Time }
			if errors.As(err, &deferred) {
				delay = max(delay, deferred.RetryAt().Sub(c.options.Now()))
			}
		}
	}
}
