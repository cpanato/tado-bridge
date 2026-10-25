package packaging_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/metrics"
	"github.com/cpanato/tado-bridge/internal/store"
)

func TestGrafanaTemplateUsesExistingMetricsAndReadOnlyQueries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "integrations", "grafana", "tado-bridge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Templating struct {
			List []struct {
				Name  string `json:"name"`
				Multi bool   `json:"multi"`
				Query struct {
					Query string `json:"query"`
				} `json:"query"`
			} `json:"list"`
		} `json:"templating"`
		Inputs []struct {
			Name   string `json:"name"`
			Plugin string `json:"pluginId"`
		} `json:"__inputs"`
		Panels []struct {
			ID         int    `json:"id"`
			Type       string `json:"type"`
			Title      string `json:"title"`
			Datasource struct {
				UID string `json:"uid"`
			} `json:"datasource"`
			Targets []struct {
				Expr string `json:"expr"`
				SQL  string `json:"rawSql"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Inputs) != 2 || dashboard.Inputs[0].Name != "DS_PROMETHEUS" || dashboard.Inputs[0].Plugin != "prometheus" || dashboard.Inputs[1].Name != "DS_POSTGRESQL" || dashboard.Inputs[1].Plugin != "grafana-postgresql-datasource" {
		t.Fatal("dashboard must let the importer choose both data sources")
	}
	response := httptest.NewRecorder()
	cache := &store.Store{}
	zero := 0.0
	if err := cache.Save(domain.Snapshot{CollectedAt: time.Now(), Homes: []domain.HomeReading{{Zones: []domain.ZoneReading{{TemperatureC: &zero, TargetTemperatureC: &zero, HumidityPercent: &zero}}}}}); err != nil {
		t.Fatal(err)
	}
	metrics.New(cache, nil, metrics.Options{}).Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	metricNames := regexp.MustCompile(`tado_bridge_[a-z_]+`)
	seen := map[int]bool{}
	roomSelector := false
	for _, variable := range dashboard.Templating.List {
		if variable.Name == "room" && !variable.Multi && strings.Contains(variable.Query.Query, "tado_bridge_current_room_info") {
			roomSelector = true
		}
	}
	if !roomSelector {
		t.Fatal("current room must be discovered from metrics")
	}
	promPanels, pgPanels := 0, 0
	for _, panel := range dashboard.Panels {
		if seen[panel.ID] {
			t.Fatal("duplicate panel ID")
		}
		seen[panel.ID] = true
		for _, target := range panel.Targets {
			if strings.Contains(target.Expr, "tado_bridge_current_") && (panel.Type != "gauge" || !strings.Contains(panel.Title, "$room") || !strings.Contains(target.Expr, `room="$room"`)) {
				t.Fatal("current gauges must identify and filter the selected room")
			}
			if target.Expr != "" {
				promPanels++
				if panel.Datasource.UID != "${DS_PROMETHEUS}" || !strings.Contains(target.Expr, `${instance:regex}`) {
					t.Fatal("Prometheus panels must isolate the selected instance")
				}
				for _, name := range metricNames.FindAllString(target.Expr, -1) {
					if !strings.Contains(response.Body.String(), "# HELP "+name+" ") {
						t.Fatalf("unknown metric %s", name)
					}
				}
			}
			if target.SQL != "" {
				pgPanels++
				if panel.Datasource.UID != "${DS_POSTGRESQL}" || !strings.HasPrefix(target.SQL, "SELECT ") || !strings.Contains(target.SQL, "$__timeFilter(s.collected_at)") || !strings.Contains(target.SQL, "$__timeGroupAlias(s.collected_at, $__interval)") {
					t.Fatal("history panel lacks bounded time-series query or selectable datasource")
				}
			}
		}
	}
	if promPanels != 7 || pgPanels != 4 {
		t.Fatalf("unexpected panels: prometheus=%d postgres=%d", promPanels, pgPanels)
	}
}

func TestDailySummaryDashboardUsesSharedReadOnlySQL(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "integrations", "grafana", "tado-bridge-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		UID    string `json:"uid"`
		Panels []struct {
			Datasource struct {
				UID string `json:"uid"`
			} `json:"datasource"`
			Targets []struct {
				SQL string `json:"rawSql"`
			} `json:"targets"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Name  string `json:"name"`
				Query string `json:"query"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.UID != "tado-bridge-summary" || len(dashboard.Panels) != 4 {
		t.Fatal("unexpected daily dashboard")
	}
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			if panel.Datasource.UID != "${DS_POSTGRESQL}" || !strings.HasPrefix(target.SQL, "SELECT ") || !strings.Contains(target.SQL, "tado_bridge_daily_summary($__timeFrom(), $__timeTo()") {
				t.Fatal("summary must reuse bounded, read-only history aggregation")
			}
			for _, variable := range []string{"summary_timezone", "summary_max_gap", "summary_home", "summary_room"} {
				if !strings.Contains(target.SQL, "${"+variable+":sqlstring}") {
					t.Fatal("summary variable must be SQL-escaped:", variable)
				}
			}
		}
	}
	found := map[string]bool{}
	for _, variable := range dashboard.Templating.List {
		found[variable.Name] = true
		if variable.Name == "summary_home" || variable.Name == "summary_room" {
			if !strings.HasPrefix(variable.Query, "SELECT ") || !strings.Contains(variable.Query, "$__timeFilter(s.collected_at)") {
				t.Fatal("room discovery must query bounded history")
			}
		}
	}
	for _, name := range []string{"summary_home", "summary_room", "summary_timezone", "summary_max_gap"} {
		if !found[name] {
			t.Fatal("missing summary selector", name)
		}
	}
}
