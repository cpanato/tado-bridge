// Package store holds one immutable serialized snapshot in memory.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/cpanato/tado-bridge/internal/domain"
)

const maxSnapshotBytes = 4 << 20

type View struct {
	JSON        []byte
	CollectedAt time.Time
	Usable      bool
}

type Store struct{ current atomic.Pointer[View] }

// Save serializes before replacement, so failures preserve the last good value.
func (s *Store) Save(snapshot domain.Snapshot) error {
	if snapshot.CollectedAt.IsZero() || snapshot.Homes == nil {
		return errors.New("incomplete snapshot")
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return errors.New("snapshot encoding failed")
	}
	if len(data) > maxSnapshotBytes {
		return errors.New("snapshot exceeds 4 MiB cache limit")
	}
	usable := false
	for _, home := range snapshot.Homes {
		for _, zone := range home.Zones {
			if zone.TemperatureC != nil || zone.HumidityPercent != nil || zone.TargetTemperatureC != nil || zone.HeatingActive != nil {
				usable = true
			}
		}
	}
	s.current.Store(&View{JSON: data, CollectedAt: snapshot.CollectedAt, Usable: usable})
	return nil
}

// Load returns an owned copy; callers cannot mutate the cached bytes.
func (s *Store) Load() (View, bool) {
	value := s.current.Load()
	if value == nil {
		return View{}, false
	}
	return View{JSON: bytes.Clone(value.JSON), CollectedAt: value.CollectedAt, Usable: value.Usable}, true
}
