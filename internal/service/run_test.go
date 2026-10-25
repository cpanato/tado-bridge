package service

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/httpapi"
	"github.com/cpanato/tado-bridge/internal/integrations/trmnl"
	"github.com/cpanato/tado-bridge/internal/store"
)

func TestLocalServiceCacheAndShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &store.Store{}
	started := make(chan struct{})
	var calls atomic.Int32
	collect := func(ctx context.Context) error {
		calls.Add(1)
		value := 21.0
		err := cache.Save(domain.Snapshot{Provider: "fixture", CollectedAt: time.Now().UTC(), Homes: []domain.HomeReading{{Zones: []domain.ZoneReading{{TemperatureC: &value}}}}})
		if err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	publisher := trmnl.New(trmnl.Config{Enabled: true, WebhookURL: "https://127.0.0.1:1/api/custom_plugins/fixture", Interval: 30 * time.Minute, Rooms: []trmnl.Room{{HomeID: "fixture", ZoneID: "fixture", Name: "Room 1"}}}, cache, time.Hour, nil, nil)
	joined := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, listener, httpapi.New(cache, httpapi.Options{MaxSnapshotAge: time.Hour}), collect, func(ctx context.Context) error { defer close(joined); return publisher.Run(ctx) })
	}()
	<-started
	client := &http.Client{Timeout: time.Second}
	for range 10 {
		for _, path := range []string{"/healthz", "/readyz", "/api/v1/snapshot"} {
			response, err := client.Get("http://" + address + path)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("status=%d", response.StatusCode)
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatal("consumer requests triggered upstream collection")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service did not stop")
	}
	select {
	case <-joined:
	default:
		t.Fatal("publisher was not joined")
	}
	client.CloseIdleConnections()
	connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("listener remained open")
	}
}

func TestListenerFailureCancelsCollector(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	stopped := make(chan struct{})
	err = Run(context.Background(), listener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), func(ctx context.Context) error { <-ctx.Done(); close(stopped); return ctx.Err() })
	if err == nil {
		t.Fatal("expected listener error")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("collector was not joined")
	}
}
