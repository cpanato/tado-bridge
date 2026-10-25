package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/history"
)

type stackClient struct {
	client *http.Client
	ctx    context.Context
}

func (c stackClient) request(address, auth string, body, destination any, allowError bool) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Method = http.MethodPost
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return 0, errors.New("local stack HTTP request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 && !allowError {
		return response.StatusCode, fmt.Errorf("local stack returned HTTP %d", response.StatusCode)
	}
	if destination != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(destination); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}

func waitFor(ctx context.Context, description string, check func() bool) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("timed out waiting for " + description)
}

type promSeries struct {
	Metric map[string]string `json:"metric"`
	Value  []json.RawMessage `json:"value"`
}

func (c stackClient) prometheus(address, query string) ([]promSeries, error) {
	var response struct {
		Status string `json:"status"`
		Data   struct {
			Result []promSeries `json:"result"`
		} `json:"data"`
	}
	_, err := c.request(address+"/api/v1/query?"+url.Values{"query": {query}}.Encode(), "", nil, &response, false)
	if err != nil {
		return nil, err
	}
	if response.Status != "success" {
		return nil, errors.New("prometheus query failed")
	}
	return response.Data.Result, nil
}

type frame struct {
	Data struct {
		Values [][]any `json:"values"`
	} `json:"data"`
}
type queryResult struct {
	Error  string  `json:"error"`
	Frames []frame `json:"frames"`
}

func (c stackClient) grafana(address, auth string, query map[string]any, allowError bool) (queryResult, error) {
	var response struct {
		Results map[string]queryResult `json:"results"`
	}
	now := time.Now()
	body := map[string]any{"from": strconv.FormatInt(now.Add(-24*time.Hour).UnixMilli(), 10), "to": strconv.FormatInt(now.UnixMilli(), 10), "queries": []any{query}}
	_, err := c.request(address+"/api/ds/query", auth, body, &response, allowError)
	if err != nil {
		return queryResult{}, err
	}
	result, ok := response.Results["A"]
	if !ok {
		return queryResult{}, errors.New("grafana query result missing")
	}
	return result, nil
}

func roomValue(value string) string {
	data, _ := json.Marshal(value)
	return string(data[1 : len(data)-1])
}

func checkStack(ctx context.Context, values map[string]string) error {
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	c := stackClient{client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, ctx: ctx}
	bridge := "http://127.0.0.1:" + values["TADO_BRIDGE_PORT"]
	prometheus := "http://127.0.0.1:" + values["PROMETHEUS_PORT"]
	grafana := "http://127.0.0.1:" + values["GRAFANA_PORT"]
	bearer := "Bearer " + values["API_TOKEN"]
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+values["GRAFANA_ADMIN_PASSWORD"]))
	if err := waitFor(ctx, "preview cache", func() bool {
		var ready struct {
			Status string `json:"status"`
		}
		_, err := c.request(bridge+"/readyz", "", nil, &ready, false)
		return err == nil && ready.Status == "ready"
	}); err != nil {
		return err
	}
	var snapshot domain.Snapshot
	if _, err := c.request(bridge+"/api/v1/snapshot", bearer, nil, &snapshot, false); err != nil {
		return err
	}
	if snapshot.Provider != "synthetic-preview" || len(snapshot.Homes) != 1 || len(snapshot.Homes[0].Zones) != 8 {
		return errors.New("unexpected synthetic snapshot")
	}
	status, err := c.request(bridge+"/metrics", "", nil, nil, true)
	if err != nil {
		return err
	}
	if status != http.StatusUnauthorized {
		return errors.New("unauthenticated metrics were accepted")
	}
	if err := waitFor(ctx, "two authenticated Prometheus scrapes", func() bool {
		series, err := c.prometheus(prometheus, `count_over_time(up{job="tado-bridge"}[1m]) >= 2`)
		return err == nil && len(series) > 0
	}); err != nil {
		return err
	}
	var dashboard struct {
		Dashboard struct {
			Panels []struct {
				Targets []map[string]any `json:"targets"`
			} `json:"panels"`
		} `json:"dashboard"`
	}
	if _, err := c.request(grafana+"/api/dashboards/uid/tado-bridge", auth, nil, &dashboard, false); err != nil {
		return err
	}
	choices, err := c.prometheus(prometheus, `tado_bridge_current_room_info{job="tado-bridge",instance="tado-bridge:8080"}`)
	if err != nil {
		return err
	}
	rooms := map[string]bool{}
	for _, item := range choices {
		rooms[item.Metric["room"]] = true
	}
	if len(rooms) != 9 || !rooms["All rooms (mean)"] {
		return errors.New("room discovery did not find all eight rooms and aggregate")
	}
	for i := 1; i <= 8; i++ {
		if !rooms[fmt.Sprintf("Synthetic home / Room %d", i)] {
			return errors.New("room discovery missed a room")
		}
	}
	count := 0
	for _, panel := range dashboard.Dashboard.Panels {
		for _, query := range panel.Targets {
			if expr, ok := query["expr"].(string); ok {
				query["expr"] = strings.NewReplacer("${job:regex}", "tado-bridge", "${instance:regex}", "tado-bridge:8080", "$room", roomValue("All rooms (mean)"), "$__range", "24h").Replace(expr)
			}
			if sql, ok := query["rawSql"].(string); ok {
				query["rawSql"] = strings.ReplaceAll(sql, "$__interval", "15m")
			}
			query["intervalMs"] = 900000
			query["maxDataPoints"] = 1000
			result, err := c.grafana(grafana, auth, query, false)
			if err != nil {
				return err
			}
			if result.Error != "" {
				return errors.New("grafana datasource query failed")
			}
			hasData := false
			for _, frame := range result.Frames {
				for _, column := range frame.Data.Values {
					if len(column) > 0 {
						hasData = true
					}
				}
			}
			if !hasData {
				return errors.New("grafana panel returned no data")
			}
			count++
		}
	}
	if count != 11 {
		return fmt.Errorf("expected 11 Grafana queries, got %d", count)
	}
	for _, zone := range snapshot.Homes[0].Zones {
		for metric, expected := range map[string]*float64{"temperature_celsius": zone.TemperatureC, "target_temperature_celsius": zone.TargetTemperatureC, "humidity_percent": zone.HumidityPercent} {
			expr := `tado_bridge_current_` + metric + `{job="tado-bridge",instance="tado-bridge:8080",room="` + roomValue("Synthetic home / "+zone.Name) + `"}`
			series, err := c.prometheus(prometheus, expr)
			if err != nil {
				return err
			}
			if err := checkGauge(series, expected); err != nil {
				return err
			}
		}
	}
	query := map[string]any{"refId": "A", "datasource": map[string]string{"uid": "tado-bridge-postgres", "type": "grafana-postgresql-datasource"}, "rawQuery": true, "rawSql": "UPDATE tado_bridge_snapshots SET payload = payload WHERE false", "format": "table"}
	denied, err := c.grafana(grafana, auth, query, true)
	if err != nil {
		return err
	}
	if denied.Error == "" {
		return errors.New("grafana database role can write history")
	}
	query["rawSql"] = "SELECT current_user, has_table_privilege(current_user, 'tado_bridge_snapshots', 'SELECT') AS can_read, has_table_privilege(current_user, 'tado_bridge_snapshots', 'INSERT,UPDATE,DELETE,TRUNCATE') AS can_write"
	privileges, err := c.grafana(grafana, auth, query, false)
	if err != nil {
		return err
	}
	if privileges.Error != "" || len(privileges.Frames) != 1 || !reflect.DeepEqual(privileges.Frames[0].Data.Values, [][]any{{"grafana_reader"}, {true}, {false}}) {
		return errors.New("unexpected Grafana role privileges")
	}
	if err := c.checkSummaries(bridge, grafana, bearer, auth, snapshot.CollectedAt); err != nil {
		return err
	}
	return c.checkAlerts(grafana, prometheus, auth)
}

func (c stackClient) checkSummaries(bridge, grafana, bearer, auth string, at time.Time) error {
	query := url.Values{"from": {at.Add(-24 * time.Hour).Format(time.RFC3339Nano)}, "to": {at.Add(time.Second).Format(time.RFC3339Nano)}, "timezone": {"UTC"}, "home_id": {"synthetic-home"}, "zone_id": {"synthetic-room-1"}}
	endpoint := bridge + "/api/v1/history/summary?" + query.Encode()
	if status, err := c.request(endpoint, "", nil, nil, true); err != nil || status != http.StatusUnauthorized {
		return errors.New("unauthenticated summaries were accepted")
	}
	var summary history.Summary
	if _, err := c.request(endpoint, bearer, nil, &summary, false); err != nil {
		return err
	}
	var samples int64
	for _, day := range summary.Days {
		if day.HomeID != "synthetic-home" || day.ZoneID != "synthetic-room-1" {
			return errors.New("summary selected the wrong room")
		}
		samples += day.TemperatureC.Samples
	}
	if samples != 97 {
		return fmt.Errorf("expected 97 summary samples, got %d", samples)
	}
	var dashboard struct {
		Dashboard struct {
			Panels []struct {
				Targets []map[string]any `json:"targets"`
			} `json:"panels"`
		} `json:"dashboard"`
	}
	if _, err := c.request(grafana+"/api/dashboards/uid/tado-bridge-summary", auth, nil, &dashboard, false); err != nil {
		return err
	}
	count := 0
	for _, panel := range dashboard.Dashboard.Panels {
		for _, target := range panel.Targets {
			sql, ok := target["rawSql"].(string)
			if !ok {
				return errors.New("summary query missing")
			}
			target["rawSql"] = strings.NewReplacer("${summary_timezone:sqlstring}", "'UTC'", "${summary_max_gap:sqlstring}", "'7200'", "${summary_home:sqlstring}", "'synthetic-home'", "${summary_room:sqlstring}", "'synthetic-room-1'").Replace(sql)
			result, err := c.grafana(grafana, auth, target, false)
			if err != nil {
				return err
			}
			if result.Error != "" || len(result.Frames) == 0 || len(result.Frames[0].Data.Values) == 0 || len(result.Frames[0].Data.Values[0]) == 0 {
				return errors.New("summary panel returned no data")
			}
			count++
		}
	}
	if count != 4 {
		return errors.New("expected four summary panel queries")
	}
	return nil
}

func (c stackClient) checkAlerts(grafana, prometheus, auth string) error {
	var rules []struct {
		UID                  string `json:"uid"`
		For                  string `json:"for"`
		NotificationSettings struct {
			MuteIntervals []string `json:"mute_time_intervals"`
			Repeat        string   `json:"repeat_interval"`
		} `json:"notification_settings"`
		Data []struct {
			RefID string `json:"refId"`
			Model struct {
				Expr string `json:"expr"`
			} `json:"model"`
		} `json:"data"`
	}
	if _, err := c.request(grafana+"/api/v1/provisioning/alert-rules", auth, nil, &rules, false); err != nil {
		return err
	}
	if len(rules) != 3 {
		return errors.New("expected three native Grafana alert rules")
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		seen[rule.UID] = true
		if len(rule.NotificationSettings.MuteIntervals) != 1 || rule.NotificationSettings.MuteIntervals[0] != "tado-bridge-ui-only" {
			return errors.New("synthetic alert notifications are not muted")
		}
		if duration, err := time.ParseDuration(rule.NotificationSettings.Repeat); err != nil || duration != 4*time.Hour {
			return errors.New("unexpected alert repeat interval")
		}
		if duration, err := time.ParseDuration(rule.For); err != nil || duration <= 0 {
			return errors.New("alert minimum duration missing")
		}
		for _, query := range rule.Data {
			if query.RefID == "A" {
				series, err := c.prometheus(prometheus, query.Model.Expr)
				if err != nil {
					return err
				}
				if len(series) == 0 {
					return errors.New("alert expression returned no series")
				}
				for _, item := range series {
					if item.Metric["room"] == "All rooms (mean)" {
						return errors.New("room alert targets the aggregate")
					}
				}
			}
		}
	}
	for _, uid := range alertUIDs {
		if !seen[uid] {
			return errors.New("native alert rule missing")
		}
	}
	// Check the actual Grafana evaluator, not just the imported JSON or PromQL.
	return waitFor(c.ctx, "healthy native Grafana alert evaluations", func() bool {
		var response struct {
			Data struct {
				Groups []struct {
					Rules []struct {
						Name           string    `json:"name"`
						Health         string    `json:"health"`
						LastError      string    `json:"lastError"`
						LastEvaluation time.Time `json:"lastEvaluation"`
					} `json:"rules"`
				} `json:"groups"`
			} `json:"data"`
		}
		if _, err := c.request(grafana+"/api/prometheus/grafana/api/v1/rules", auth, nil, &response, false); err != nil {
			return false
		}
		count := 0
		for _, group := range response.Data.Groups {
			for _, rule := range group.Rules {
				if strings.HasPrefix(rule.Name, "tado-bridge") {
					if rule.Health != "ok" || rule.LastError != "" || rule.LastEvaluation.IsZero() {
						return false
					}
					count++
				}
			}
		}
		return count == 3
	})
}

func checkGauge(series []promSeries, expected *float64) error {
	if expected == nil {
		if len(series) != 0 {
			return errors.New("missing room reading became a value")
		}
		return nil
	}
	if len(series) != 1 || len(series[0].Value) != 2 {
		return errors.New("expected exactly one selected room reading")
	}
	var raw string
	if err := json.Unmarshal(series[0].Value[1], &raw); err != nil {
		return err
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value != *expected {
		return errors.New("gauge selected the wrong room")
	}
	return nil
}
