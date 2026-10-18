package tado

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/credentials"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New()
	c.http = server.Client()
	c.apiURL, c.authURL, c.xAPIURL = server.URL, server.URL, server.URL
	return c
}

func TestDiscovery(t *testing.T) {
	var paths []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Error("wrong method or authorization")
		}
		if r.URL.Path == "/homes/123" || r.URL.Path == "/homes/456" {
			_, _ = io.WriteString(w, `{"generation":"PRE_LINE_X"}`)
			return
		}
		fixture := "zones.json"
		if r.URL.Path == "/me" {
			fixture = "me.json"
		}
		data, err := os.ReadFile("testdata/" + fixture)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data)
	})
	result, err := c.Discover(context.Background(), credentials.Token{AccessToken: "fixture-access"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/me", "/homes/123", "/homes/123/zones", "/homes/456", "/homes/456/zones"}) || c.APIRequests != 5 {
		t.Fatalf("paths=%v count=%d", paths, c.APIRequests)
	}
	data, _ := json.Marshal(result)
	expected := `{"provider":"tado","homes":[{"id":"123","name":"Example home","zones":[{"id":"1","name":"Living room"},{"id":"2","name":"Bedroom"}]},{"id":"456","name":"Other home","zones":[{"id":"1","name":"Living room"},{"id":"2","name":"Bedroom"}]}]}`
	if string(data) != expected {
		t.Fatalf("unexpected discovery: %s", data)
	}
}

func TestDiscoveryFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{"error":"private-secret"}`},
		{"quota", 429, `{"error":"private-secret"}`},
		{"malformed", 200, `private-secret`},
		{"missing ID", 200, `{"homes":[{"name":"Home"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := c.Discover(context.Background(), credentials.Token{})
			if err == nil || strings.Contains(err.Error(), "private-secret") || c.APIRequests != 1 {
				t.Fatalf("err=%v calls=%d", err, c.APIRequests)
			}
		})
	}
}

func TestDeviceLogin(t *testing.T) {
	polls := 0
	var waits []time.Duration
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error("expected POST")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("client_id") != clientID {
			t.Error("missing client ID")
		}
		if r.URL.Path == "/device_authorize" {
			if r.PostForm.Get("scope") != "offline_access" {
				t.Error("missing offline scope")
			}
			_, _ = io.WriteString(w, `{"device_code":"fixture-device","user_code":"ABCDEF","verification_uri":"https://login.tado.com/oauth2/device","verification_uri_complete":"https://login.tado.com/oauth2/device?user_code=ABCDEF&flow=fixture","expires_in":300,"interval":1}`)
			return
		}
		if r.PostForm.Get("device_code") != "fixture-device" || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
			t.Error("wrong grant")
		}
		polls++
		if polls < 3 {
			w.WriteHeader(400)
			code := "authorization_pending"
			if polls == 2 {
				code = "slow_down"
			}
			_, _ = io.WriteString(w, `{"error":"`+code+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":600,"token_type":"Bearer"}`)
	})
	c.wait = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	shown := false
	token, err := c.Login(context.Background(), func(uri, code string) error {
		parsed, err := url.Parse(uri)
		shown = err == nil && parsed.Host == "login.tado.com" && parsed.Query().Get("client_id") == clientID && parsed.Query().Get("user_code") == "ABCDEF" && parsed.Query().Get("flow") == "fixture" && code == "ABCDEF"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !shown || token.RefreshToken != "fixture-refresh" || c.OAuthRequests != 4 || !reflect.DeepEqual(waits, []time.Duration{time.Second, time.Second, 6 * time.Second}) {
		t.Fatalf("shown=%t calls=%d waits=%v", shown, c.OAuthRequests, waits)
	}
}

func TestRefresh(t *testing.T) {
	for _, refresh := range []string{"rotated", ""} {
		t.Run("refresh="+refresh, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.PostForm.Get("refresh_token") != "old" || r.PostForm.Get("grant_type") != "refresh_token" {
					t.Error("wrong refresh grant")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": refresh, "expires_in": 600, "token_type": "Bearer"})
			})
			now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			c.now = func() time.Time { return now }
			result, err := c.Refresh(context.Background(), credentials.Token{RefreshToken: "old"})
			if err != nil {
				t.Fatal(err)
			}
			expected := refresh
			if expected == "" {
				expected = "old"
			}
			if result.RefreshToken != expected || !result.ExpiresAt.Equal(now.Add(600*time.Second)) {
				t.Fatal("incorrect refreshed credentials")
			}
		})
	}
}

func TestLoginTerminalErrors(t *testing.T) {
	for _, code := range []string{"access_denied", "expired_token", "invalid_grant"} {
		t.Run(code, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/device_authorize" {
					_, _ = io.WriteString(w, `{"device_code":"d","user_code":"u","verification_uri":"https://login.tado.com/oauth2/device","expires_in":300}`)
					return
				}
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
			})
			c.wait = func(context.Context, time.Duration) error { return nil }
			if _, err := c.Login(context.Background(), func(string, string) error { return nil }); err == nil || c.OAuthRequests != 2 {
				t.Fatalf("err=%v calls=%d", err, c.OAuthRequests)
			}
		})
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c.http.Timeout = 10 * time.Millisecond
	_, err := c.Discover(context.Background(), credentials.Token{})
	if err == nil || strings.Contains(err.Error(), c.apiURL) {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = c.Discover(ctx, credentials.Token{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEmptyHomes(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"homes":[]}`) })
	result, err := c.Discover(context.Background(), credentials.Token{})
	if err != nil || result.Homes == nil || len(result.Homes) != 0 {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestVerificationURL(t *testing.T) {
	for _, tc := range []struct {
		name, complete string
		invalid        bool
	}{
		{name: "bare URI fallback"},
		{name: "complete URI without client ID", complete: "https://login.tado.com/oauth2/device?user_code=A%2BB&flow=fixture"},
		{name: "complete URI with client ID", complete: "https://login.tado.com/oauth2/device?client_id=" + clientID + "&user_code=A%2BB&flow=fixture"},
		{name: "HTTP rejected", complete: "http://login.tado.com/oauth2/device", invalid: true},
		{name: "foreign host rejected", complete: "https://example.invalid/device", invalid: true},
		{name: "userinfo rejected", complete: "https://user@login.tado.com/oauth2/device", invalid: true},
		{name: "fragment rejected", complete: "https://login.tado.com/oauth2/device#fragment", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := deviceCode{VerificationURI: "https://login.tado.com/oauth2/device", VerificationURIComplete: tc.complete, UserCode: "A+B"}
			verification, err := device.verificationURL()
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid verification URL")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(verification)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Query().Get("client_id") != clientID || parsed.Query().Get("user_code") != "A+B" {
				t.Fatal("missing or corrupted browser parameters")
			}
			if tc.complete != "" && parsed.Query().Get("flow") != "fixture" {
				t.Fatal("lost provider parameter")
			}
		})
	}
}

func TestMixedGenerationDiscovery(t *testing.T) {
	var paths []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Error("wrong method or bearer token")
		}
		switch r.URL.Path {
		case "/me":
			data, err := os.ReadFile("testdata/me.json")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(data)
		case "/homes/123":
			_, _ = io.WriteString(w, `{"generation":"PRE_LINE_X"}`)
		case "/homes/456":
			_, _ = io.WriteString(w, `{"generation":"LINE_X"}`)
		case "/homes/123/zones":
			data, err := os.ReadFile("testdata/zones.json")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(data)
		default:
			t.Error("unexpected V3 route")
			http.NotFound(w, r)
		}
	})
	xCalls := 0
	xServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		xCalls++
		if r.Method != "GET" || r.URL.Path != "/homes/456/roomsAndDevices" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Error("wrong X request")
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile("testdata/rooms_x.json")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data)
	}))
	defer xServer.Close()
	c.xAPIURL = xServer.URL
	result, err := c.Discover(context.Background(), credentials.Token{AccessToken: "fixture-access"})
	if err != nil {
		t.Fatal(err)
	}
	if c.APIRequests != 5 || xCalls != 1 || !reflect.DeepEqual(paths, []string{"/me", "/homes/123", "/homes/123/zones", "/homes/456"}) {
		t.Fatalf("unexpected routing: paths=%v API=%d X=%d", paths, c.APIRequests, xCalls)
	}
	if result.Provider != "tado" || len(result.Homes) != 2 || len(result.Homes[0].Zones) != 2 || len(result.Homes[1].Zones) != 8 {
		t.Fatal("unexpected home or zone counts")
	}
	if result.Homes[1].Zones[0].ID != "1" || result.Homes[1].Zones[0].Name != "Room 1" {
		t.Fatal("X room did not normalize")
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE", "secret@", "devices", "connection", "generation", "roomId", "roomName"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("discovery exposed upstream metadata: %s", private)
		}
	}
}

func TestZoneDiscoveryResponses(t *testing.T) {
	for _, tc := range []struct {
		name, generation, body string
		status                 int
		wantError              bool
	}{
		{"empty V3", "PRE_LINE_X", `[]`, 200, false},
		{"legacy V3 without generation", "", `[]`, 200, false},
		{"null V3", "PRE_LINE_X", `null`, 200, true},
		{"missing V3 ID", "PRE_LINE_X", `[{"name":"Room"}]`, 200, true},
		{"empty X", "LINE_X", `{"rooms":[]}`, 200, false},
		{"missing X rooms", "LINE_X", `{}`, 200, true},
		{"null X rooms", "LINE_X", `{"rooms":null}`, 200, true},
		{"missing X ID", "LINE_X", `{"rooms":[{"roomName":"Room"}]}`, 200, true},
		{"invalid X JSON", "LINE_X", `private-secret`, 200, true},
		{"wrong X schema", "LINE_X", `[]`, 200, true},
		{"X unauthorized", "LINE_X", `{"error":"private-secret"}`, 401, true},
		{"X quota", "LINE_X", `{"error":"private-secret"}`, 429, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/homes/123" {
					_ = json.NewEncoder(w).Encode(map[string]string{"generation": tc.generation})
					return
				}
				expected := "/homes/123/zones"
				if tc.generation == "LINE_X" {
					expected = "/homes/123/roomsAndDevices"
				}
				if r.URL.Path != expected {
					t.Error("unexpected route")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			zones, err := c.discoverZones(context.Background(), "fixture-access", "123")
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("error exposed response body")
			}
			if err == nil && (zones == nil || len(zones) != 0) {
				t.Fatal("empty list did not normalize")
			}
			if c.APIRequests != 2 {
				t.Fatalf("unexpected retries/fallback: %d", c.APIRequests)
			}
		})
	}
}

func TestHomeGenerationFailureStopsDiscovery(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/me" {
			_, _ = io.WriteString(w, `{"homes":[{"id":123,"name":"Home"}]}`)
			return
		}
		if r.URL.Path != "/homes/123" {
			t.Error("should not fetch zones after generation failure")
		}
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := c.Discover(context.Background(), credentials.Token{AccessToken: "fixture-access"})
	if err == nil || c.APIRequests != 2 {
		t.Fatalf("err=%v calls=%d", err, c.APIRequests)
	}
}
