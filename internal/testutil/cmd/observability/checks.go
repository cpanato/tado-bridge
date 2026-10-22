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
	return nil
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
