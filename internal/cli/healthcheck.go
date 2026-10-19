package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cpanato/tado-bridge/internal/service"
)

func healthcheckCommand() *cobra.Command {
	return &cobra.Command{
		Use: "healthcheck", Short: "Check the configured local service liveness without credentials", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := service.LoadConfig(os.Getenv)
			if err != nil {
				return err
			}
			host, port, _ := net.SplitHostPort(config.ListenAddr)
			if host == "" || host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			if host == "::" {
				host = "::1"
			}
			if port == "0" {
				return errors.New("healthcheck requires a fixed LISTEN_ADDR port")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz", nil)
			if err != nil {
				return errors.New("invalid healthcheck address")
			}
			transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return errors.New("local service healthcheck failed")
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("healthcheck returned HTTP %d", response.StatusCode)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "healthy")
			return err
		},
	}
}
