// Package trmnl publishes selected cached climate data to an opt-in private plugin.
package trmnl

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Room struct {
	HomeID string `json:"home_id"`
	ZoneID string `json:"zone_id"`
	Name   string `json:"name"`
}

type Config struct {
	Enabled    bool
	WebhookURL string
	Interval   time.Duration
	Rooms      []Room
}

// LoadConfig ignores all destination/selection settings while disabled.
// Errors deliberately exclude values, since the URL is a bearer secret.
func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{Interval: 30 * time.Minute}
	if value := getenv("TRMNL_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, errors.New("TRMNL_ENABLED must be a boolean")
		}
		c.Enabled = enabled
	}
	if !c.Enabled {
		return c, nil
	}
	c.WebhookURL = getenv("TRMNL_WEBHOOK_URL")
	u, err := url.Parse(c.WebhookURL)
	if err != nil || u.Scheme != "https" || (u.Host != "trmnl.com" && u.Host != "usetrmnl.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return Config{}, errors.New("TRMNL_WEBHOOK_URL must be an official HTTPS private-plugin webhook URL")
	}
	secret := strings.TrimPrefix(u.Path, "/api/custom_plugins/")
	if secret == u.Path || secret == "" || len(secret) > 128 || strings.ContainsAny(secret, "/\\") {
		return Config{}, errors.New("TRMNL_WEBHOOK_URL requires a private-plugin webhook path")
	}
	for _, r := range secret {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !allowed {
			return Config{}, errors.New("TRMNL_WEBHOOK_URL has an invalid webhook path")
		}
	}
	if value := getenv("TRMNL_INTERVAL"); value != "" {
		c.Interval, err = time.ParseDuration(value)
		if err != nil || c.Interval < 5*time.Minute || c.Interval > 24*time.Hour {
			return Config{}, errors.New("TRMNL_INTERVAL must be between 5m and 24h")
		}
	}
	raw := getenv("TRMNL_ROOMS")
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &c.Rooms) != nil || len(c.Rooms) == 0 || len(c.Rooms) > 16 {
		return Config{}, errors.New("TRMNL_ROOMS must be a JSON array selecting 1 to 16 rooms")
	}
	seen := make(map[[2]string]bool)
	for i := range c.Rooms {
		room := &c.Rooms[i]
		key := [2]string{room.HomeID, room.ZoneID}
		if room.HomeID == "" || room.ZoneID == "" || len(room.HomeID) > 128 || len(room.ZoneID) > 128 || seen[key] {
			return Config{}, errors.New("TRMNL_ROOMS requires unique nonempty home_id and zone_id values")
		}
		seen[key] = true
		if room.Name == "" {
			room.Name = "Room " + strconv.Itoa(i+1)
		}
		if !utf8.ValidString(room.Name) || len(room.Name) > 48 {
			return Config{}, errors.New("TRMNL_ROOMS display names must be valid UTF-8 of at most 48 bytes")
		}
		for _, r := range room.Name {
			if r < 32 || r == 127 {
				return Config{}, errors.New("TRMNL_ROOMS display names cannot contain control characters")
			}
		}
	}
	return c, nil
}
