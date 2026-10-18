package tado

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/domain"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRead(t *testing.T) {
	for _, generation := range []string{"LINE_X", "PRE_LINE_X"} {
		t.Run(generation, func(t *testing.T) {
			var paths []string
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-access" {
					t.Error("read must only issue authorized GET requests")
				}
				switch r.URL.Path {
				case "/me":
					_, _ = io.WriteString(w, `{"homes":[{"id":123,"name":"Example home","email":"PRIVATE-EMAIL"}]}`)
				case "/homes/123":
					_ = json.NewEncoder(w).Encode(map[string]string{"generation": generation})
				case "/homes/123/rooms":
					if generation != "LINE_X" {
						t.Error("unexpected X call")
					}
					_, _ = w.Write(fixture(t, "readings_x.json"))
				case "/homes/123/zones":
					if generation == "LINE_X" {
						t.Error("unexpected V3 call")
					}
					_, _ = w.Write(fixture(t, "zones.json"))
				case "/homes/123/zones/1/state", "/homes/123/zones/2/state":
					_, _ = w.Write(fixture(t, "state_v3.json"))
				default:
					t.Error("unexpected route")
					http.NotFound(w, r)
				}
			})
			now := time.Date(2026, 10, 18, 20, 5, 0, 0, time.FixedZone("offset", 7200))
			c.now = func() time.Time { return now }
			result, err := c.Read(context.Background(), credentials.Token{AccessToken: "fixture-access"})
			if err != nil {
				t.Fatal(err)
			}
			expected := []string{"/me", "/homes/123", "/homes/123/rooms"}
			count := 3
			if generation != "LINE_X" {
				expected = []string{"/me", "/homes/123", "/homes/123/zones", "/homes/123/zones/1/state", "/homes/123/zones/2/state"}
				count = 2
			}
			if !reflect.DeepEqual(paths, expected) || c.APIRequests != len(expected) {
				t.Fatalf("paths=%v count=%d", paths, c.APIRequests)
			}
			if result.Provider != "tado" || !result.CollectedAt.Equal(now) || result.CollectedAt.Location() != time.UTC || len(result.Homes) != 1 || len(result.Homes[0].Zones) != count {
				t.Fatalf("unexpected collection metadata")
			}
			first := result.Homes[0].Zones[0]
			if first.ID != "1" || first.Name != "Living room" {
				t.Fatal("invalid identity")
			}
			assertFloat(t, first.TemperatureC, 21.5)
			assertFloat(t, first.TargetTemperatureC, 22)
			assertFloat(t, first.HumidityPercent, 48)
			if first.HeatingActive == nil || !*first.HeatingActive {
				t.Fatal("expected explicit heating power to indicate active")
			}
			if generation == "LINE_X" {
				if first.TemperatureMeasuredAt != nil {
					t.Fatal("fabricated measurement timestamp")
				}
				second := result.Homes[0].Zones[1]
				assertFloat(t, second.TemperatureC, 0)
				assertFloat(t, second.HumidityPercent, 0)
				if second.TargetTemperatureC != nil || second.HeatingActive == nil || *second.HeatingActive {
					t.Fatal("OFF target/zero heating did not normalize")
				}
				missing := result.Homes[0].Zones[2]
				if missing.TemperatureC != nil || missing.HumidityPercent != nil || missing.HeatingActive != nil {
					t.Fatal("missing sensors must remain unknown")
				}
			} else {
				if first.TemperatureMeasuredAt == nil || first.TemperatureMeasuredAt.Format(time.RFC3339) != "2026-10-18T18:00:00Z" {
					t.Fatal("measurement timestamp must preserve upstream time in UTC")
				}
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"PRIVATE", "sensorDataPoints", "connection", "fahrenheit"} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("exposed upstream field: %s", secret)
				}
			}
			if generation == "LINE_X" && (!strings.Contains(string(data), `"temperature_c":null`) || !strings.Contains(string(data), `"heating_active":false`)) {
				t.Fatal("null and false lost in JSON")
			}
		})
	}
}

func assertFloat(t *testing.T, actual *float64, expected float64) {
	t.Helper()
	if actual == nil || *actual != expected {
		t.Fatalf("expected %g, got %v", expected, actual)
	}
}

func TestReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, generation, body string
		status                 int
	}{
		{"X quota", "LINE_X", `{"error":"PRIVATE"}`, 429},
		{"X malformed", "LINE_X", `PRIVATE`, 200},
		{"X null", "LINE_X", `null`, 200},
		{"X no ID", "LINE_X", `[{"name":"Room"}]`, 200},
		{"X wrong number", "LINE_X", `[{"id":1,"sensorDataPoints":{"insideTemperature":{"value":"PRIVATE"}}}]`, 200},
		{"V3 state error", "PRE_LINE_X", `{"error":"PRIVATE"}`, 503},
		{"V3 state null", "PRE_LINE_X", `null`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/me":
					_, _ = io.WriteString(w, `{"homes":[{"id":123,"name":"Home"}]}`)
				case "/homes/123":
					_ = json.NewEncoder(w).Encode(map[string]string{"generation": tc.generation})
				case "/homes/123/zones":
					_, _ = io.WriteString(w, `[{"id":1,"name":"Room"}]`)
				default:
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}
			})
			result, err := c.Read(context.Background(), credentials.Token{})
			if err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Homes != nil || !result.CollectedAt.IsZero() {
				t.Fatal("failed collection returned partial data")
			}
			expected := 3
			if tc.generation != "LINE_X" {
				expected = 4
			}
			if c.APIRequests != expected {
				t.Fatal("unexpected retry/fallback")
			}
		})
	}
}

func TestNormalizeUnknownHeating(t *testing.T) {
	measured, target := 21.0, 22.0
	result := normalize(domain.Zone{}, sensors{InsideTemperature: temperature{Value: &measured}}, setting{Temperature: temperature{Value: &target}}, heatingPower{}, true)
	if result.HeatingActive != nil {
		t.Fatal("must not infer heating from measured/target temperatures")
	}
	for _, invalid := range []float64{-1, 101} {
		result = normalize(domain.Zone{}, sensors{}, setting{}, heatingPower{Percentage: &invalid}, true)
		if result.HeatingActive != nil {
			t.Fatal("invalid heating power must be unknown")
		}
	}
}

func TestReadEmptyHomes(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"homes":[]}`) })
	result, err := c.Read(context.Background(), credentials.Token{})
	if err != nil || result.Homes == nil || len(result.Homes) != 0 || c.APIRequests != 1 {
		t.Fatalf("err=%v calls=%d", err, c.APIRequests)
	}
}

func TestReadEmptyZones(t *testing.T) {
	for _, generation := range []string{"LINE_X", "PRE_LINE_X"} {
		t.Run(generation, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/me":
					_, _ = io.WriteString(w, `{"homes":[{"id":123,"name":"Home"}]}`)
				case "/homes/123":
					_ = json.NewEncoder(w).Encode(map[string]string{"generation": generation})
				default:
					_, _ = io.WriteString(w, `[]`)
				}
			})
			result, err := c.Read(context.Background(), credentials.Token{})
			if err != nil || len(result.Homes) != 1 || result.Homes[0].Zones == nil || len(result.Homes[0].Zones) != 0 || c.APIRequests != 3 {
				t.Fatalf("unexpected empty result: err=%v calls=%d", err, c.APIRequests)
			}
		})
	}
}

func TestReadDoesNotReturnEarlierHomesOnFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			_, _ = w.Write(fixture(t, "me.json"))
		case "/homes/123", "/homes/456":
			_, _ = io.WriteString(w, `{"generation":"LINE_X"}`)
		case "/homes/123/rooms":
			_, _ = w.Write(fixture(t, "readings_x.json"))
		case "/homes/456/rooms":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Error("unexpected route")
			http.NotFound(w, r)
		}
	})
	result, err := c.Read(context.Background(), credentials.Token{})
	if err == nil || result.Homes != nil || !result.CollectedAt.IsZero() || c.APIRequests != 5 {
		t.Fatal("failed collection must discard earlier home results")
	}
}
