package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

var alertUIDs = []string{"tado-low-temperature", "tado-high-humidity", "tado-stale-collection"}

// Alert rules are opt-in and visible only in Grafana. A per-rule, all-day mute
// prevents outbound notifications without replacing any user policy tree.
func prepareAlerts(root, directory string, getenv func(string) string) error {
	if err := privateDirectory(filepath.Join(directory, "alerting")); err != nil {
		return err
	}
	enabled := false
	if value := getenv("GRAFANA_ALERTS_ENABLED"); value != "" {
		var err error
		enabled, err = strconv.ParseBool(value)
		if err != nil {
			return errors.New("GRAFANA_ALERTS_ENABLED must be a boolean")
		}
	}
	path := filepath.Join(directory, "alerting/tado-bridge.json")
	if !enabled {
		deletions := []any{}
		for _, uid := range alertUIDs {
			deletions = append(deletions, map[string]any{"orgId": 1, "uid": uid})
		}
		return writeJSON(path, map[string]any{"apiVersion": 1, "deleteRules": deletions})
	}
	number := func(name string, defaultValue, min, max float64) (float64, error) {
		value := getenv(name)
		if value == "" {
			return defaultValue, nil
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < min || parsed > max {
			return 0, fmt.Errorf("%s must be a finite number between %g and %g", name, min, max)
		}
		return parsed, nil
	}
	duration := func(name string, defaultValue, min, max time.Duration) (time.Duration, error) {
		value := getenv(name)
		if value == "" {
			return defaultValue, nil
		}
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < min || parsed > max {
			return 0, fmt.Errorf("%s must be a duration between %s and %s", name, min, max)
		}
		return parsed, nil
	}
	low, err := number("GRAFANA_ALERTS_LOW_TEMPERATURE_C", 18, -50, 60)
	if err != nil {
		return err
	}
	high, err := number("GRAFANA_ALERTS_HIGH_HUMIDITY_PERCENT", 70, 0, 100)
	if err != nil {
		return err
	}
	pending, err := duration("GRAFANA_ALERTS_FOR", 90*time.Minute, time.Minute, 7*24*time.Hour)
	if err != nil {
		return err
	}
	stalePending, err := duration("GRAFANA_ALERTS_STALE_FOR", 5*time.Minute, time.Minute, 24*time.Hour)
	if err != nil {
		return err
	}
	age, err := duration("MAX_SNAPSHOT_AGE", 2*time.Hour, time.Minute, 7*24*time.Hour)
	if err != nil {
		return err
	}
	repeat, err := duration("GRAFANA_ALERTS_REPEAT_INTERVAL", 4*time.Hour, 5*time.Minute, 7*24*time.Hour)
	if err != nil {
		return err
	}
	if repeat%(5*time.Minute) != 0 {
		return errors.New("GRAFANA_ALERTS_REPEAT_INTERVAL must be a multiple of 5m")
	}
	data, err := os.ReadFile(filepath.Join(root, "integrations/grafana/alerts.json"))
	if err != nil {
		return err
	}
	var template struct {
		APIVersion int `json:"apiVersion"`
		Groups     []struct {
			OrgID    int              `json:"orgId"`
			Name     string           `json:"name"`
			Folder   string           `json:"folder"`
			Interval string           `json:"interval"`
			Rules    []map[string]any `json:"rules"`
		} `json:"groups"`
		MuteTimes []json.RawMessage `json:"muteTimes"`
	}
	if err := json.Unmarshal(data, &template); err != nil {
		return err
	}
	replacements := map[string]string{
		"tado-low-temperature":  fmt.Sprintf(`(tado_bridge_current_temperature_celsius{job="tado-bridge",room!="All rooms (mean)"} < bool %g) and on(job,instance) (tado_bridge_snapshot_age_seconds{job="tado-bridge"} <= %g)`, low, age.Seconds()),
		"tado-high-humidity":    fmt.Sprintf(`(tado_bridge_current_humidity_percent{job="tado-bridge",room!="All rooms (mean)"} > bool %g) and on(job,instance) (tado_bridge_snapshot_age_seconds{job="tado-bridge"} <= %g)`, high, age.Seconds()),
		"tado-stale-collection": fmt.Sprintf(`tado_bridge_snapshot_age_seconds{job="tado-bridge"} > bool %g`, age.Seconds()),
	}
	for _, group := range template.Groups {
		for _, rule := range group.Rules {
			uid, _ := rule["uid"].(string)
			expr, ok := replacements[uid]
			if !ok {
				return errors.New("unknown alert template rule")
			}
			rule["for"] = pending.String()
			if uid == "tado-stale-collection" {
				rule["for"] = stalePending.String()
			}
			settings, ok := rule["notification_settings"].(map[string]any)
			if !ok {
				return errors.New("alert notification settings missing")
			}
			settings["repeat_interval"] = repeat.String()
			targets, ok := rule["data"].([]any)
			if !ok {
				return errors.New("alert query missing")
			}
			for _, raw := range targets {
				target, ok := raw.(map[string]any)
				if !ok {
					return errors.New("invalid alert query")
				}
				if target["refId"] == "A" {
					model, ok := target["model"].(map[string]any)
					if !ok {
						return errors.New("alert query model missing")
					}
					model["expr"] = expr
				}
			}
		}
	}
	return writeJSON(path, template)
}
