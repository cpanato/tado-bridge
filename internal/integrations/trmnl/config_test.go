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
