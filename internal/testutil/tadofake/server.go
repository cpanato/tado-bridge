// Package tadofake provides a deterministic, synthetic tado API for integration tests.
// It never calls upstream services and must not be used with real credentials.
package tadofake

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

const AccessToken = "fixture-access"
const InitialRefreshToken = "fixture-refresh"
const RotatedRefreshToken = "fixture-rotated-refresh"

type Server struct {
	unavailable atomic.Bool
	api         atomic.Int64
	oauth       atomic.Int64
}

type Stats struct {
	API   int64 `json:"api"`
	OAuth int64 `json:"oauth"`
}

func (s *Server) Stats() Stats              { return Stats{API: s.api.Load(), OAuth: s.oauth.Load()} }
func (s *Server) SetUnavailable(value bool) { s.unavailable.Store(value) }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/healthz" {
		write(w, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/__fixture/stats" {
		write(w, s.Stats())
		return
	}
	if r.URL.Path == "/__fixture/failure" && r.Method == http.MethodPost {
		s.SetUnavailable(r.URL.Query().Get("enabled") == "true")
		write(w, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/oauth2/token" && r.Method == http.MethodPost {
		s.oauth.Add(1)
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") == "" || (r.Form.Get("refresh_token") != InitialRefreshToken && r.Form.Get("refresh_token") != RotatedRefreshToken) {
			http.Error(w, "invalid fixture refresh", 400)
			return
		}
		write(w, map[string]any{"access_token": AccessToken, "refresh_token": RotatedRefreshToken, "expires_in": 3600, "token_type": "Bearer"})
		return
	}
	s.api.Add(1)
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+AccessToken {
		w.WriteHeader(401)
		return
	}
	if s.unavailable.Load() {
		w.WriteHeader(503)
		return
	}
	switch r.URL.Path {
	case "/api/v2/me":
		write(w, map[string]any{"homes": []map[string]any{{"id": 1, "name": "Fixture V3"}, {"id": 2, "name": "Fixture X"}}})
	case "/api/v2/homes/1":
		write(w, map[string]string{"generation": "V3"})
	case "/api/v2/homes/2":
		write(w, map[string]string{"generation": "LINE_X"})
	case "/api/v2/homes/1/zones":
		write(w, []map[string]any{{"id": 1, "name": "Fixture V3 room"}})
	case "/api/v2/homes/1/zones/1/state":
		write(w, map[string]any{"sensorDataPoints": map[string]any{"insideTemperature": map[string]any{"celsius": 0}, "humidity": map[string]any{"percentage": 0}}, "setting": map[string]any{"power": "OFF", "temperature": map[string]any{"celsius": 18}}, "activityDataPoints": map[string]any{"heatingPower": map[string]any{"percentage": 0}}})
	case "/homes/2/rooms", "/homes/2/roomsAndDevices":
		rooms := make([]map[string]any, 8)
		for i := range rooms {
			rooms[i] = map[string]any{"id": i + 1, "name": "Fixture room", "sensorDataPoints": map[string]any{"insideTemperature": map[string]any{"value": 20 + float64(i)/2}, "humidity": map[string]any{"percentage": 45}}, "setting": map[string]any{"power": "ON", "temperature": map[string]any{"value": 22}}, "heatingPower": map[string]any{"percentage": 35}}
		}
		if r.URL.Path == "/homes/2/roomsAndDevices" {
			write(w, map[string]any{"rooms": rooms})
		} else {
			write(w, rooms)
		}
	default:
		w.WriteHeader(404)
	}
}
func write(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
