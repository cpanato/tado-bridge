package service

import (
	"strings"
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	config, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.ListenAddr != "127.0.0.1:8080" || config.PollInterval != 30*time.Minute || config.MaxSnapshotAge != 2*time.Hour || config.DailyBudget != 90 {
		t.Fatalf("defaults: %+v", config)
	}
	for _, tc := range []struct {
		name  string
		env   map[string]string
		valid bool
	}{
		{"IPv6 loopback", map[string]string{"LISTEN_ADDR": "[::1]:8080"}, true},
		{"localhost", map[string]string{"LISTEN_ADDR": "localhost:8080"}, true},
		{"loopback dynamic port", map[string]string{"LISTEN_ADDR": "127.0.0.1:0"}, true},
		{"public without acknowledgement", map[string]string{"LISTEN_ADDR": "0.0.0.0:8080", "API_TOKEN": "secret"}, false},
		{"public without token", map[string]string{"LISTEN_ADDR": "0.0.0.0:8080", "ALLOW_NETWORK_EXPOSURE": "true"}, false},
		{"public explicit opt-in", map[string]string{"LISTEN_ADDR": "0.0.0.0:8080", "ALLOW_NETWORK_EXPOSURE": "true", "API_TOKEN": "secret"}, true},
		{"hostname", map[string]string{"LISTEN_ADDR": "example.invalid:8080"}, false},
		{"no host", map[string]string{"LISTEN_ADDR": ":8080"}, false},
		{"bad port", map[string]string{"LISTEN_ADDR": "127.0.0.1:65536"}, false},
		{"fast poll", map[string]string{"POLL_INTERVAL": "1m"}, false},
		{"invalid poll", map[string]string{"POLL_INTERVAL": "secret-value"}, false},
		{"zero age", map[string]string{"MAX_SNAPSHOT_AGE": "0s"}, false},
		{"invalid budget", map[string]string{"TADO_DAILY_REQUEST_BUDGET": "0"}, false},
		{"large budget", map[string]string{"TADO_DAILY_REQUEST_BUDGET": "1000001"}, false},
		{"invalid bool", map[string]string{"ALLOW_NETWORK_EXPOSURE": "maybe"}, false},
		{"token whitespace", map[string]string{"API_TOKEN": "secret value"}, false},
		{"metrics enabled", map[string]string{"METRICS_ENABLED": "true"}, true},
		{"invalid metrics boolean", map[string]string{"METRICS_ENABLED": "invalid"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(func(name string) string { return tc.env[name] })
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected configuration result: %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "secret value")) {
				t.Fatal("configuration error leaked value")
			}
		})
	}
}
