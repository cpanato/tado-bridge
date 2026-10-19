package trmnl

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

func fixture(t *testing.T, now time.Time) *store.Store {
	t.Helper()
	zero := 0.0
	off := false
	cache := &store.Store{}
	if err := cache.Save(domain.Snapshot{Provider: "private-provider", CollectedAt: now, Homes: []domain.HomeReading{{ID: "private-home", Name: "private-home-name", Zones: []domain.ZoneReading{{Zone: domain.Zone{ID: "private-zone", Name: "private-zone-name"}, TemperatureC: &zero, HeatingActive: &off}, {Zone: domain.Zone{ID: "unselected", Name: "private-unselected"}}}}}}); err != nil {
		t.Fatal(err)
	}
	return cache
}
func TestPayloadSelectionFreshnessAndPrivacy(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	view, _ := fixture(t, now).Load()
	rooms := []Room{{HomeID: "private-home", ZoneID: "private-zone", Name: "Room 1"}, {HomeID: "private-home", ZoneID: "missing", Name: "Room 2"}}
	for _, age := range []time.Duration{0, 3 * time.Hour} {
		data, stale, err := payload(view, rooms, now.Add(age), 2*time.Hour)
		if err != nil || stale != (age > 2*time.Hour) {
			t.Fatalf("stale=%v err=%v", stale, err)
		}
		if strings.Contains(string(data), "private") || strings.Contains(string(data), "unselected") {
			t.Fatal("identifiers or private names leaked")
		}
		var parsed struct {
			Variables variables `json:"merge_variables"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		v := parsed.Variables
		if len(v.Rooms) != 2 || !v.Rooms[0].Available || v.Rooms[0].Temperature == nil || *v.Rooms[0].Temperature != 0 || v.Rooms[0].Target != nil || v.Rooms[0].Humidity != nil || v.Rooms[0].Heating == nil || *v.Rooms[0].Heating || v.Rooms[1].Available {
			t.Fatalf("incorrect optional readings: %s", data)
		}
		if !v.CollectedAt.Equal(now) || !v.PublishedAt.Equal(now.Add(age)) || v.Stale != stale || v.Source != "tado-bridge" {
			t.Fatal("incorrect provenance")
		}
	}
	oversized := make([]Room, 16)
	for i := range oversized {
		oversized[i].Name = strings.Repeat("<", 48)
	}
	if _, _, err := payload(view, oversized, now, time.Hour); err == nil {
		t.Fatal("oversized payload accepted")
	}
}
