package metrics

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/cpanato/tado-bridge/internal/domain"
)

const AllRooms = "All rooms (mean)"
const maxDetailedRooms = 128

type currentDescriptors struct{ info, temperature, target, humidity, omitted *prometheus.Desc }

func newCurrentDescriptors() currentDescriptors {
	return currentDescriptors{
		info:        prometheus.NewDesc("tado_bridge_current_room_info", "Current room choices; the aggregate is always available after collection, room names require explicit opt-in.", []string{"room"}, nil),
		temperature: prometheus.NewDesc("tado_bridge_current_temperature_celsius", "Available cached temperature for a discovered room or the aggregate mean.", []string{"room"}, nil),
		target:      prometheus.NewDesc("tado_bridge_current_target_temperature_celsius", "Available cached active target for a discovered room or the aggregate mean.", []string{"room"}, nil),
		humidity:    prometheus.NewDesc("tado_bridge_current_humidity_percent", "Available cached humidity for a discovered room or the aggregate mean.", []string{"room"}, nil),
		omitted:     prometheus.NewDesc("tado_bridge_room_details_omitted", "Discovered rooms beyond the per-snapshot detail limit; zero when room details are disabled.", nil, nil),
	}
}

func (c *snapshotCollector) collectCurrent(output chan<- prometheus.Metric, snapshot domain.Snapshot, temperature, target, humidity aggregate) {
	emit := func(desc *prometheus.Desc, value float64, room string) {
		output <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, room)
	}
	emit(c.current.info, 1, AllRooms)
	for _, item := range []struct {
		desc  *prometheus.Desc
		value aggregate
	}{{c.current.temperature, temperature}, {c.current.target, target}, {c.current.humidity, humidity}} {
		if item.value.count > 0 {
			emit(item.desc, item.value.sum/float64(item.value.count), AllRooms)
		}
	}
	type reading struct {
		homeID, zoneID, label string
		zone                  domain.ZoneReading
	}
	var rooms []reading
	if c.roomDetails {
		for _, home := range snapshot.Homes {
			for _, zone := range home.Zones {
				rooms = append(rooms, reading{home.ID, zone.ID, roomName(home.Name, home.ID) + " / " + roomName(zone.Name, zone.ID), zone})
			}
		}
	}
	// Stable ordering keeps the limit independent of upstream discovery ordering.
	slices.SortFunc(rooms, func(a, b reading) int {
		if n := cmp.Compare(a.homeID, b.homeID); n != 0 {
			return n
		}
		return cmp.Compare(a.zoneID, b.zoneID)
	})
	counts := map[string]int{}
	for _, room := range rooms {
		counts[room.label]++
	}
	omitted := max(0, len(rooms)-maxDetailedRooms)
	output <- prometheus.MustNewConstMetric(c.current.omitted, prometheus.GaugeValue, float64(omitted))
	used := map[string]bool{AllRooms: true}
	for _, room := range rooms[:min(len(rooms), maxDetailedRooms)] {
		label := room.label
		if counts[label] > 1 {
			label += fmt.Sprintf(" [%q/%q]", roomName(room.homeID, "home"), roomName(room.zoneID, "room"))
		}
		base := label
		for i := 2; used[label]; i++ {
			label = fmt.Sprintf("%s [%d]", base, i)
		}
		used[label] = true
		emit(c.current.info, 1, label)
		for _, item := range []struct {
			desc  *prometheus.Desc
			value *float64
		}{{c.current.temperature, room.zone.TemperatureC}, {c.current.target, room.zone.TargetTemperatureC}, {c.current.humidity, room.zone.HumidityPercent}} {
			if item.value != nil {
				emit(item.desc, *item.value, label)
			}
		}
	}
}

func roomName(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	if len(name) > 128 {
		name = name[:128]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name
}
