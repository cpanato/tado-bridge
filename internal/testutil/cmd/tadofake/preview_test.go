package main

import (
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/store"
)

func TestPreviewUsesSyntheticDataWithNullableReadings(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	snapshots := previewSnapshots(now)
	if len(snapshots) != 97 || !snapshots[0].CollectedAt.Equal(now.Add(-24*time.Hour)) || !snapshots[96].CollectedAt.Equal(now) {
		t.Fatal("preview must cover one day")
	}
	cache := &store.Store{}
	for _, snapshot := range snapshots {
		if snapshot.Provider != "synthetic-preview" || len(snapshot.Homes) != 1 || len(snapshot.Homes[0].Zones) != 8 {
			t.Fatal("unexpected fixture structure")
		}
		if err := cache.Save(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if snapshots[0].Homes[0].Zones[1].TargetTemperatureC != nil || snapshots[0].Homes[0].Zones[6].HumidityPercent != nil {
		t.Fatal("missing values must stay null")
	}
	if *snapshots[0].Homes[0].Zones[0].TemperatureC == *snapshots[24].Homes[0].Zones[0].TemperatureC {
		t.Fatal("preview does not exercise time-varying data")
	}
}
