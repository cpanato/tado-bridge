// Package service manages configuration and the local HTTP/collector lifecycle.
package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Run starts collection, HTTP serving and optional background tasks together,
// then cancels and joins every task before return.
func Run(ctx context.Context, listener net.Listener, handler http.Handler, collect func(context.Context) error, background ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = listener.Close() }()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	completed := make(chan error, 2+len(background))
	go func() { completed <- collect(ctx) }()
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		completed <- err
	}()
	for _, run := range background {
		go func() { completed <- run(ctx) }()
	}
	var result error
	pending := 2 + len(background)
	select {
	case <-ctx.Done():
	case result = <-completed:
		pending--
	}
	cancel()
	if errors.Is(result, context.Canceled) {
		result = nil
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		result = errors.Join(result, err)
	}
	for range pending {
		err := <-completed
		if !errors.Is(err, context.Canceled) {
			result = errors.Join(result, err)
		}
	}
	return result
}
