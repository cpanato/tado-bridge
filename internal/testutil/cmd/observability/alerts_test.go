package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlertPreparationOptInBoundsAndNoExternalChannels(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if err := prepareAlerts(root, dir, getenv); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "alerting/tado-bridge.json")
	read := func() map[string]any {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	data := read()
	if data["groups"] != nil || len(data["deleteRules"].([]any)) != 3 {
		t.Fatal("disabled alerts are still enabled")
	}
	env["GRAFANA_ALERTS_ENABLED"] = "true"
	env["GRAFANA_ALERTS_LOW_TEMPERATURE_C"] = "17.5"
	env["GRAFANA_ALERTS_HIGH_HUMIDITY_PERCENT"] = "75"
	env["MAX_SNAPSHOT_AGE"] = "3h"
	env["GRAFANA_ALERTS_FOR"] = "2h"
	env["GRAFANA_ALERTS_REPEAT_INTERVAL"] = "6h"
	if err := prepareAlerts(root, dir, getenv); err != nil {
		t.Fatal(err)
	}
	data = read()
	if data["policies"] != nil || data["contactPoints"] != nil {
		t.Fatal("helper overwrites external notification configuration")
	}
	rules := data["groups"].([]any)[0].(map[string]any)["rules"].([]any)
	if len(rules) != 3 {
		t.Fatal("missing native rules")
	}
	for _, raw := range rules {
		rule := raw.(map[string]any)
		settings := rule["notification_settings"].(map[string]any)
		if settings["repeat_interval"] != "6h0m0s" || settings["mute_time_intervals"].([]any)[0] != "tado-bridge-ui-only" {
			t.Fatal("notifications are not muted or interval changed")
		}
		expr := rule["data"].([]any)[0].(map[string]any)["model"].(map[string]any)["expr"].(string)
		if !strings.Contains(expr, "10800") {
			t.Fatal("collection freshness not configurable")
		}
		if rule["uid"] != "tado-stale-collection" && (rule["for"] != "2h0m0s" || !strings.Contains(expr, `room!="All rooms (mean)"`)) {
			t.Fatal("room alert timing/filter missing")
		}
	}
	for _, tc := range []struct{ key, value string }{
		{"GRAFANA_ALERTS_ENABLED", "maybe"}, {"GRAFANA_ALERTS_LOW_TEMPERATURE_C", "NaN"},
		{"GRAFANA_ALERTS_LOW_TEMPERATURE_C", "+Inf"}, {"GRAFANA_ALERTS_HIGH_HUMIDITY_PERCENT", "101"},
		{"GRAFANA_ALERTS_FOR", "0s"}, {"GRAFANA_ALERTS_STALE_FOR", "bad"}, {"MAX_SNAPSHOT_AGE", "-1h"},
		{"GRAFANA_ALERTS_REPEAT_INTERVAL", "6m"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			old := env[tc.key]
			env[tc.key] = tc.value
			defer func() { env[tc.key] = old }()
			if prepareAlerts(root, dir, getenv) == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("unsafe alert configuration permissions")
	}
}
