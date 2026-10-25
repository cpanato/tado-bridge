package trmnl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/history"
)

type summaryStub struct {
	calls  int
	query  history.SummaryQuery
	result history.Summary
	err    error
}

func (s *summaryStub) Summarize(_ context.Context, q history.SummaryQuery) (history.Summary, error) {
	s.calls++
	s.query = q
	return s.result, s.err
}

func TestSummaryOptInSelectionPrivacyAndFailureIsolation(t *testing.T) {
	now := time.Now().UTC()
	cache := fixture(t, now)
	view, _ := cache.Load()
	value, delta := 19.23, -0.25
	reader := &summaryStub{result: history.Summary{Days: []history.DailySummary{
		{Date: now.Format("2006-01-02"), HomeID: "private-home", ZoneID: "private-zone", TemperatureC: history.Statistics{Min: &value, Max: &value, Mean: &value, Samples: 3}, TemperatureMeanChangeC: &delta},
		{Date: now.Format("2006-01-02"), HomeID: "unselected", ZoneID: "private-other", ZoneName: "PRIVATE_OTHER_ROOM", TemperatureC: history.Statistics{Mean: &value, Samples: 3}},
	}}}
	config := Config{Enabled: true, Rooms: []Room{{HomeID: "private-home", ZoneID: "private-zone", Name: "Room 1"}}, SummaryTimezone: "Europe/Berlin"}
	p := New(config, cache, time.Hour, nil, reader)
	data, _, err := p.payload(t.Context(), view)
	if err != nil || reader.calls != 0 || strings.Contains(string(data), "summary") {
		t.Fatal("summary accessed without opt-in", err)
	}
	p.config.SummaryEnabled = true
	// Use UTC for a deterministic match with the fixture's day label.
	p.config.SummaryTimezone = "UTC"
	data, _, err = p.payload(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private") || strings.Contains(string(data), "PRIVATE_OTHER_ROOM") {
		t.Fatal("private summary identifiers leaked")
	}
	var parsed struct {
		Variables variables `json:"merge_variables"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	summary := parsed.Variables.Rooms[0].Summary
	if summary == nil || *summary.Mean != 19.2 || *summary.MeanChange != -0.3 || parsed.Variables.SummaryTimezone != "UTC" {
		t.Fatalf("incorrect compact summary: %s", data)
	}
	if reader.calls != 1 || !reader.query.To.Equal(now.Add(time.Microsecond)) || reader.query.Timezone != "UTC" {
		t.Fatal("summary not anchored to the published snapshot")
	}
	reader.err = errors.New("PRIVATE_DATABASE_SECRET")
	data, _, err = p.payload(t.Context(), view)
	if err != nil || strings.Contains(string(data), "summary") || !strings.Contains(string(data), "temperature_c") {
		t.Fatal("history failure discarded current readings", err)
	}
}

func TestSummaryDeduplicatedWithSnapshot(t *testing.T) {
	now := time.Now().UTC()
	p := testPublisher(t, fixture(t, now), func(_ http.ResponseWriter, _ *http.Request) {})
	reader := &summaryStub{}
	p.summaries = reader
	p.config.SummaryEnabled = true
	p.config.SummaryTimezone = "UTC"
	waits := 0
	p.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 2 {
			return context.Canceled
		}
		return nil
	}
	_ = p.Run(t.Context())
	if reader.calls != 1 {
		t.Fatalf("duplicate snapshot queried history %d times", reader.calls)
	}
}

func TestSummaryPayloadBoundFallsBackToCurrent(t *testing.T) {
	now := time.Now().UTC()
	cache := fixture(t, now)
	view, _ := cache.Load()
	reader := &summaryStub{}
	config := Config{Enabled: true, SummaryEnabled: true, SummaryTimezone: "UTC"}
	value := 123456789012345.0
	for i := range 16 {
		id := fmt.Sprint(i)
		config.Rooms = append(config.Rooms, Room{HomeID: "h", ZoneID: id, Name: strings.Repeat("<", 12)})
		reader.result.Days = append(reader.result.Days, history.DailySummary{Date: now.Format("2006-01-02"), HomeID: "h", ZoneID: id, TemperatureC: history.Statistics{Min: &value, Max: &value, Mean: &value, Samples: 1}, TemperatureMeanChangeC: &value})
	}
	p := New(config, cache, time.Hour, nil, reader)
	data, _, err := p.payload(t.Context(), view)
	if err != nil || len(data) > maxPayloadBytes || strings.Contains(string(data), "summary") {
		t.Fatalf("oversized summaries did not preserve baseline payload (%d bytes): %v", len(data), err)
	}
}
