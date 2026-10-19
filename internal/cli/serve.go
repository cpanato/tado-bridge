package cli

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/cpanato/tado-bridge/internal/collector"
	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/httpapi"
	"github.com/cpanato/tado-bridge/internal/integrations/trmnl"
	"github.com/cpanato/tado-bridge/internal/metrics"
	"github.com/cpanato/tado-bridge/internal/service"
	"github.com/cpanato/tado-bridge/internal/store"
	"github.com/cpanato/tado-bridge/internal/tado"
)

func (app application) serve(cmd *cobra.Command, client *tado.Client) error {
	config, err := service.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	trmnlConfig, err := trmnl.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	// Check local credentials before opening a listener; refresh happens in collection.
	if _, err := credentials.Load(app.credentialsFile); err != nil {
		return err
	}
	budget, err := collector.NewBudget(config.DailyBudget, nil)
	if err != nil {
		return err
	}
	client.SetRequestGuard(budget.Take)
	cache := &store.Store{}
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	var exporter *metrics.Exporter
	var metricsHandler http.Handler
	var observe func(error)
	if config.MetricsEnabled {
		exporter = metrics.New(cache, nil)
		metricsHandler = exporter.Handler()
		observe = exporter.ObserveCollection
	}
	read := func(ctx context.Context) (domain.Snapshot, int, error) {
		apiBefore, oauthBefore := client.APIRequests, client.OAuthRequests
		token, err := app.token(ctx, client)
		var snapshot domain.Snapshot
		if err == nil {
			snapshot, err = client.Read(ctx, token)
		}
		api, oauth := client.APIRequests-apiBefore, client.OAuthRequests-oauthBefore
		logger.Info("upstream requests", "api", api, "oauth", oauth)
		if exporter != nil {
			exporter.ObserveRequests(api, oauth)
		}
		// Reserve a refresh on every future cycle, even when the current token was valid.
		return snapshot, api + max(oauth, 1), err
	}
	poller, err := collector.New(read, cache, collector.Options{Interval: config.PollInterval, Budget: budget, Logger: logger, ObserveCollection: observe})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", config.ListenAddr)
	if err != nil {
		return err
	}
	logger.Info("HTTP service started", "listen", listener.Addr().String(), "requested_poll_interval", config.PollInterval, "request_budget", config.DailyBudget)
	handler := httpapi.New(cache, httpapi.Options{MaxSnapshotAge: config.MaxSnapshotAge, APIToken: config.APIToken, MetricsHandler: metricsHandler})
	if trmnlConfig.Enabled {
		publisher := trmnl.New(trmnlConfig, cache, config.MaxSnapshotAge, logger)
		return service.Run(cmd.Context(), listener, handler, poller.Run, publisher.Run)
	}
	return service.Run(cmd.Context(), listener, handler, poller.Run)
}
