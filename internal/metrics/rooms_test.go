package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

func samples(t *testing.T, exporter *Exporter, name string) map[string]float64 {
	t.Helper()
	families, err := exporter.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			room := ""
			for _, label := range metric.Label {
				if label.GetName() == "room" {
					room = label.GetValue()
				}
			}
			values[room] = metric.GetGauge().GetValue()
		}
	}
	return values
}

func TestDiscoveredRoomsAreOptionalAndPreserveNullAndZero(t *testing.T) {
	cache := &store.Store{}
	zero, high, humidity := 0.0, 24.0, 50.0
	snapshot := domain.Snapshot{CollectedAt: time.Now(), Homes: []domain.HomeReading{{ID: "private-home", Name: "Home", Zones: []domain.ZoneReading{
		{Zone: domain.Zone{ID: "1", Name: "Room 1"}, TemperatureC: &zero, TargetTemperatureC: &high},
		{Zone: domain.Zone{ID: "2", Name: "Room 2"}, TemperatureC: &high, HumidityPercent: &humidity},
	}}}}
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	aggregate := New(cache, nil, Options{})
	if values := samples(t, aggregate, "tado_bridge_current_room_info"); len(values) != 1 || values[AllRooms] != 1 {
		t.Fatal("room names must require opt-in", values)
	}
	detailed := New(cache, nil, Options{RoomDetails: true})
	if values := samples(t, detailed, "tado_bridge_current_room_info"); len(values) != 3 {
		t.Fatal("missing discovered rooms", values)
	}
	values := samples(t, detailed, "tado_bridge_current_temperature_celsius")
	if len(values) != 3 || values["Home / Room 1"] != 0 || values["Home / Room 2"] != 24 || values[AllRooms] != 12 {
		t.Fatal("room selection or mean changed readings", values)
	}
	values = samples(t, detailed, "tado_bridge_current_target_temperature_celsius")
	if _, exists := values["Home / Room 2"]; exists {
		t.Fatal("missing target became a value")
	}
	values = samples(t, detailed, "tado_bridge_current_humidity_percent")
	if _, exists := values["Home / Room 1"]; exists {
		t.Fatal("missing humidity became a value")
	}
	// Discovery follows the current cache without retaining deleted room series.
	snapshot.Homes[0].Zones = snapshot.Homes[0].Zones[1:]
	snapshot.Homes[0].Zones[0].Name = "New room"
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	values = samples(t, detailed, "tado_bridge_current_room_info")
	if len(values) != 2 || values["Home / New room"] != 1 {
		t.Fatal("discovery did not follow cache replacement", values)
	}
}

func TestRoomDiscoveryDisambiguatesAndBoundsSeries(t *testing.T) {
	cache := &store.Store{}
	snapshot := domain.Snapshot{CollectedAt: time.Now(), Homes: []domain.HomeReading{{ID: "home", Name: "Home", Zones: []domain.ZoneReading{}}}}
	for i := range maxDetailedRooms + 5 {
		snapshot.Homes[0].Zones = append(snapshot.Homes[0].Zones, domain.ZoneReading{Zone: domain.Zone{ID: fmt.Sprintf("%03d", i), Name: strings.Repeat("測", 100)}})
	}
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	exporter := New(cache, nil, Options{RoomDetails: true})
	values := samples(t, exporter, "tado_bridge_current_room_info")
	if len(values) != maxDetailedRooms+1 {
		t.Fatal("unbounded or conflicting series", len(values))
	}
	for label := range values {
		if len(label) > 320 {
			t.Fatal("label not bounded")
		}
	}
	if values := samples(t, exporter, "tado_bridge_room_details_omitted"); values[""] != 5 {
		t.Fatal("truncation not observable", values)
	}
}
