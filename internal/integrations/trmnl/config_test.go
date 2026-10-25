package trmnl

import (
	"strings"
	"testing"
	"time"
)

func TestConfigPrivacyAndOptIn(t *testing.T) {
	base := map[string]string{"TRMNL_ENABLED": "true", "TRMNL_WEBHOOK_URL": "https://trmnl.com/api/custom_plugins/secret-uuid", "TRMNL_ROOMS": `[{"home_id":"private-home","zone_id":"private-zone"}]`}
	tests := []struct {
		name, key, value string
		bad              bool
	}{
		{"valid", "", "", false},
		{"disabled ignores secrets", "TRMNL_ENABLED", "false", false},
		{"bad bool", "TRMNL_ENABLED", "sure", true},
		{"http", "TRMNL_WEBHOOK_URL", "http://trmnl.com/api/custom_plugins/secret-uuid", true},
		{"wrong host", "TRMNL_WEBHOOK_URL", "https://trmnl.com.evil.test/api/custom_plugins/secret-uuid", true},
		{"query", "TRMNL_WEBHOOK_URL", "https://trmnl.com/api/custom_plugins/secret-uuid?token=private", true},
		{"userinfo", "TRMNL_WEBHOOK_URL", "https://private@trmnl.com/api/custom_plugins/secret-uuid", true},
		{"wrong path", "TRMNL_WEBHOOK_URL", "https://trmnl.com/api/private", true},
		{"escaped path", "TRMNL_WEBHOOK_URL", "https://trmnl.com/api/custom_plugins/%73ecret-uuid", true},
		{"no rooms", "TRMNL_ROOMS", "[]", true},
		{"bad rooms", "TRMNL_ROOMS", "private", true},
		{"duplicates", "TRMNL_ROOMS", `[{"home_id":"private-home","zone_id":"private-zone"},{"home_id":"private-home","zone_id":"private-zone"}]`, true},
		{"missing id", "TRMNL_ROOMS", `[{"name":"private"}]`, true},
		{"short interval", "TRMNL_INTERVAL", "1m", true},
		{"huge interval", "TRMNL_INTERVAL", "25h", true},
		{"invalid interval", "TRMNL_INTERVAL", "private", true},
		{"summary opt in", "TRMNL_HISTORY_SUMMARY", "true", false},
		{"invalid summary bool", "TRMNL_HISTORY_SUMMARY", "maybe", true},
		{"display name", "TRMNL_ROOMS", `[{"home_id":"private-home","zone_id":"private-zone","name":"Kitchen"}]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := make(map[string]string)
			for k, v := range base {
				env[k] = v
			}
			if tt.key != "" {
				env[tt.key] = tt.value
			}
			config, err := LoadConfig(func(k string) string { return env[k] })
			if (err != nil) != tt.bad {
				t.Fatalf("err=%v", err)
			}
			if err != nil {
				for _, secret := range []string{"secret-uuid", "private-home", "private-zone"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatal("secret leaked")
					}
				}
				return
			}
			if tt.name == "valid" && (config.Interval != 30*time.Minute || config.Rooms[0].Name != "Room 1") {
				t.Fatalf("unexpected defaults: %+v", config.Rooms)
			}
		})
	}
	config, err := LoadConfig(func(string) string { return "" })
	if err != nil || config.Enabled {
		t.Fatal("not disabled by default")
	}
}

func TestSummaryTimezoneRequiresItsOwnOptIn(t *testing.T) {
	env := map[string]string{"TRMNL_ENABLED": "true", "TRMNL_WEBHOOK_URL": "https://trmnl.com/api/custom_plugins/synthetic", "TRMNL_ROOMS": `[{"home_id":"h","zone_id":"z"}]`, "TRMNL_HISTORY_SUMMARY": "true", "TRMNL_SUMMARY_TIMEZONE": "Europe/Berlin"}
	read := func(k string) string { return env[k] }
	config, err := LoadConfig(read)
	if err != nil || !config.SummaryEnabled || config.SummaryTimezone != "Europe/Berlin" {
		t.Fatal("summary timezone not configured", err)
	}
	for _, zone := range []string{"Local", "Invalid/PrivateZone"} {
		env["TRMNL_SUMMARY_TIMEZONE"] = zone
		if _, err := LoadConfig(read); err == nil || strings.Contains(err.Error(), zone) {
			t.Fatal("invalid timezone accepted or echoed")
		}
	}
	env["TRMNL_HISTORY_SUMMARY"] = "false"
	if config, err := LoadConfig(read); err != nil || config.SummaryEnabled {
		t.Fatal("disabled summaries should ignore timezone", err)
	}
}
