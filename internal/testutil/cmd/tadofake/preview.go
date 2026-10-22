package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/history"
	"github.com/cpanato/tado-bridge/internal/httpapi"
	"github.com/cpanato/tado-bridge/internal/metrics"
	"github.com/cpanato/tado-bridge/internal/service"
	"github.com/cpanato/tado-bridge/internal/store"
)

// The preview always uses port 8080 in its disposable Compose stack. Probe only
// local readiness, without credentials, proxies or redirects.
func previewHealthcheck(address string) error {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(address)
	if err != nil {
		return errors.New("preview readiness check failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("preview readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}

// preview is a test-only data source for the optional Compose stack. It never
// loads OAuth credentials, creates a tado client or publishes to TRMNL.
func preview() error {
	config, err := service.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	if config.HistoryDatabaseURL == "" || config.APIToken == "" {
		return errors.New("preview requires a disposable history database and API_TOKEN")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	database, err := history.Open(ctx, config.HistoryDatabaseURL, config.HistoryRetention)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	cache := &store.Store{}
	exporter := metrics.New(cache, nil, metrics.Options{RoomDetails: config.MetricsRoomDetails})
	for _, snapshot := range previewSnapshots(time.Now().UTC().Truncate(time.Minute)) {
		if err := database.Save(ctx, snapshot); err != nil {
			return err
		}
		exporter.ObserveHistory(nil)
		if err := cache.Save(snapshot); err != nil {
			return err
		}
		exporter.ObserveCollection(nil)
	}
	listener, err := net.Listen("tcp", config.ListenAddr)
	if err != nil {
		return err
	}
	handler := httpapi.New(cache, httpapi.Options{APIToken: config.APIToken, MaxSnapshotAge: config.MaxSnapshotAge, History: database, MetricsHandler: exporter.Handler()})
	return service.Run(ctx, listener, handler, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
}

func previewSnapshots(now time.Time) []domain.Snapshot {
	result := make([]domain.Snapshot, 0, 97)
	for step := 0; step <= 96; step++ {
		at := now.Add(time.Duration(step-96) * 15 * time.Minute)
		home := domain.HomeReading{ID: "synthetic-home", Name: "Synthetic home", Zones: []domain.ZoneReading{}}
		for room := 0; room < 8; room++ {
			phase := float64(step)*math.Pi/48 + float64(room)*0.6
			temp := math.Round((19.2+float64(room)*0.45+math.Sin(phase)*1.1)*10) / 10
			target := 20.5 + float64(room%3)*0.5
			humidity := math.Round(43 + float64(room)*1.5 + math.Sin(phase+0.8)*4)
			active := (step+room*3)%16 < 7
			measured := at.Add(-3 * time.Minute)
			zone := domain.ZoneReading{Zone: domain.Zone{ID: fmt.Sprintf("synthetic-room-%d", room+1), Name: fmt.Sprintf("Room %d", room+1)}, TemperatureC: &temp, TargetTemperatureC: &target, HumidityPercent: &humidity, HeatingActive: &active, TemperatureMeasuredAt: &measured}
			if room == 1 {
				zone.TargetTemperatureC = nil
			}
			if room == 6 {
				zone.HumidityPercent = nil
			}
			home.Zones = append(home.Zones, zone)
		}
		result = append(result, domain.Snapshot{Provider: "synthetic-preview", CollectedAt: at, Homes: []domain.HomeReading{home}})
	}
	return result
}
