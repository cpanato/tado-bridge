package trmnl

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/cpanato/tado-bridge/internal/history"
	"github.com/cpanato/tado-bridge/internal/store"
)

type Publisher struct {
	config    Config
	cache     *store.Store
	maxAge    time.Duration
	client    *http.Client
	logger    *slog.Logger
	summaries history.Summarizer
	now       func() time.Time
	wait      func(context.Context, time.Duration) error
	jitter    func(time.Duration) time.Duration
}

func New(config Config, cache *store.Store, maxAge time.Duration, logger *slog.Logger, summaries history.Summarizer) *Publisher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Publisher{config: config, cache: cache, maxAge: maxAge, logger: logger, summaries: summaries, now: time.Now, wait: wait,
		jitter: func(d time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(d/4) + 1)) },
		client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// Run serializes all sends. Destination failures never stop the collector/API.
// A successful snapshot is sent once, then once more if it becomes stale.
func (p *Publisher) Run(ctx context.Context) error {
	if !p.config.Enabled {
		return nil
	}
	defer p.client.CloseIdleConnections()
	var last time.Time
	var lastStale bool
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		view, ok := p.cache.Load()
		delay := p.config.Interval
		if !ok {
			delay = 5 * time.Second
		} else {
			stale := p.now().Sub(view.CollectedAt) > p.maxAge
			if view.CollectedAt.Equal(last) && stale == lastStale {
				if err := p.wait(ctx, delay); err != nil {
					return err
				}
				continue
			}
			data, stale, err := p.payload(ctx, view)
			if err != nil {
				p.log("TRMNL payload rejected", 0)
			} else if !view.CollectedAt.Equal(last) || stale != lastStale {
				status, retryAfter, err := p.send(ctx, data)
				if err == nil {
					last, lastStale = view.CollectedAt, stale
					failures = 0
					p.log("TRMNL snapshot published", status)
				} else {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					p.log("TRMNL publish failed", status)
					if status == http.StatusTooManyRequests || status == 0 || status >= 500 {
						if failures < 3 {
							delay = 30 * time.Second * time.Duration(1<<failures)
							delay += p.jitter(delay)
							failures++
						} else {
							failures = 0
						}
						if status == http.StatusTooManyRequests && retryAfter.IsZero() {
							retryAfter = p.now().Add(time.Hour)
						}
						if !retryAfter.IsZero() {
							delay = max(delay, retryAfter.Sub(p.now()))
						}
					} else {
						failures = 0
					}
				}
			}
		}
		if err := p.wait(ctx, delay); err != nil {
			return err
		}
	}
}

func (p *Publisher) send(ctx context.Context, data []byte) (int, time.Time, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.WebhookURL, bytes.NewReader(data))
	if err != nil {
		return 0, time.Time{}, errors.New("TRMNL request creation failed")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return 0, time.Time{}, errors.New("TRMNL request failed")
	}
	defer func() { _ = response.Body.Close() }()
	status := response.StatusCode
	if status >= 200 && status < 300 {
		return status, time.Time{}, nil
	}
	var retry time.Time
	value := response.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		retry = p.now().Add(time.Duration(seconds) * time.Second)
	} else if parsed, err := http.ParseTime(value); err == nil {
		retry = parsed
	}
	return status, retry, errors.New("TRMNL returned an unsuccessful status")
}

func (p *Publisher) log(message string, status int) {
	if p.logger != nil {
		p.logger.Info(message, "status", status)
	}
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
