package store

import (
	"bytes"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
)

func TestImmutableReplacementAndFailure(t *testing.T) {
	cache := &Store{}
	if _, ok := cache.Load(); ok {
		t.Fatal("empty cache has a snapshot")
	}
	value := 0.0
	snapshot := domain.Snapshot{Provider: "fixture", CollectedAt: time.Now().UTC(), Homes: []domain.HomeReading{{Name: "Home", Zones: []domain.ZoneReading{{TemperatureC: &value}}}}}
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	first, _ := cache.Load()
	if !first.Usable {
		t.Fatal("zero is a usable measurement")
	}
	value = 100
	snapshot.Homes[0].Name = "changed"
	second, _ := cache.Load()
	if !bytes.Equal(first.JSON, second.JSON) || bytes.Contains(second.JSON, []byte("changed")) {
		t.Fatal("input mutation changed cached snapshot")
	}
	first.JSON[0] = '!'
	third, _ := cache.Load()
	if third.JSON[0] != '{' {
		t.Fatal("caller mutated cache")
	}
	value = math.NaN()
	if err := cache.Save(snapshot); err == nil {
		t.Fatal("NaN should fail encoding")
	}
	snapshot.Homes[0].Name = strings.Repeat("x", maxSnapshotBytes)
	value = 21
	if err := cache.Save(snapshot); err == nil {
		t.Fatal("oversized snapshot accepted")
	}
	retained, _ := cache.Load()
	if !bytes.Equal(third.JSON, retained.JSON) {
		t.Fatal("failed save discarded last good snapshot")
	}
	snapshot.Homes[0].Name = "replacement"
	if err := cache.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	replaced, _ := cache.Load()
	if !bytes.Contains(replaced.JSON, []byte("replacement")) {
		t.Fatal("successful save did not replace cache")
	}
}

func TestConcurrentReadAndReplace(t *testing.T) {
	cache := &Store{}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 20 {
				if err := cache.Save(domain.Snapshot{Provider: "fixture", CollectedAt: time.Now(), Homes: []domain.HomeReading{}}); err != nil {
					t.Error(err)
				}
				view, ok := cache.Load()
				if !ok || view.Usable {
					t.Error("empty homes should be cached but unusable")
				}
			}
		})
	}
	group.Wait()
}
