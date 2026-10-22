package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWaitStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitFor(ctx, "unavailable service", func() bool { return false }); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestPreparePrivateState(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepare(root, dir, "synthetic-token"); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filepath.Join(dir, "api-token"))
	if err != nil || string(token) != "synthetic-token" {
		t.Fatalf("token: %q, %v", token, err)
	}
	for _, path := range []string{"api-token", "dashboards/tado-bridge.json"} {
		info, err := os.Stat(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions: %v", path, info.Mode())
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "dashboards/tado-bridge.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard map[string]any
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if _, ok := dashboard["__inputs"]; ok || strings.Contains(string(data), "${DS_") || !strings.Contains(string(data), "tado-bridge-prometheus") || !strings.Contains(string(data), "tado-bridge-postgres") {
		t.Fatal("unmapped datasource")
	}
	sentinel := filepath.Join(dir, "grafana/existing-state")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepare(root, dir, "rotated-token"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatal("existing state lost")
	}
}

func TestPrepareRejectsUnsafeState(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"symlink-file", "symlink-directory", "shared-directory", "empty-token", "whitespace-token", "unicode-token"} {
		t.Run(scenario, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "state")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "unrelated")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			token := "synthetic-token"
			switch scenario {
			case "symlink-file":
				err = os.Symlink(target, filepath.Join(dir, "api-token"))
			case "symlink-directory":
				err = os.Symlink(parent, filepath.Join(dir, "grafana"))
			case "shared-directory":
				err = os.Chmod(dir, 0o755)
			case "empty-token":
				token = ""
			case "whitespace-token":
				token = "secret\n"
			case "unicode-token":
				token = "測"
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := prepare(root, dir, token); err == nil {
				t.Fatal("unsafe preparation accepted")
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
				t.Fatal("unrelated target modified")
			}
		})
	}
}

func TestStopOnlyRetainedProject(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-purpose", "wrong-project", "cleanup-failed"} {
		t.Run(scenario, func(t *testing.T) {
			dir, err := os.MkdirTemp(t.TempDir(), "tado-observability-test-")
			if err != nil {
				t.Fatal(err)
			}
			state := previewState{Purpose: purpose, Project: filepath.Base(dir), Environment: map[string]string{"API_TOKEN": "synthetic"}}
			if scenario == "wrong-purpose" {
				state.Purpose = "other"
			}
			if scenario == "wrong-project" {
				state.Project = "unrelated"
			}
			stateFile := filepath.Join(dir, "preview-state.json")
			if err := writeJSON(stateFile, state); err != nil {
				t.Fatal(err)
			}
			called := false
			runner := func(root string, env map[string]string, args []string, capture bool) ([]byte, error) {
				called = true
				if !reflect.DeepEqual(env, state.Environment) || capture || root != "/repo" {
					t.Fatal("incorrect execution context")
				}
				expected := composeArgs(root, dir, state.Project, "down", "--volumes", "--timeout", "15")
				if !reflect.DeepEqual(args, expected) {
					t.Fatal("incorrect cleanup target")
				}
				if scenario == "cleanup-failed" {
					return nil, errors.New("Docker unavailable")
				}
				return nil, nil
			}
			err = stop("/repo", stateFile, runner)
			valid := scenario == "valid"
			if (err == nil) != valid {
				t.Fatalf("unexpected cleanup result: %v", err)
			}
			if called != (valid || scenario == "cleanup-failed") {
				t.Fatal("unrelated project cleanup attempted")
			}
			_, err = os.Stat(dir)
			if valid != os.IsNotExist(err) {
				t.Fatal("temporary state removed before successful cleanup")
			}
		})
	}
}

func TestRoomFilterAndNullableGauge(t *testing.T) {
	for _, value := range []string{"All rooms (mean)", `Home / Room [1].* + "quoted"`, `Home / 測\room`} {
		var decoded string
		if err := json.Unmarshal([]byte(`"`+roomValue(value)+`"`), &decoded); err != nil || decoded != value {
			t.Fatal("room name changed")
		}
	}
	zero := 0.0
	series := []promSeries{{Value: []json.RawMessage{json.RawMessage(`0`), json.RawMessage(`"0"`)}}}
	if checkGauge(nil, nil) != nil || checkGauge(series, &zero) != nil {
		t.Fatal("null/zero reading rejected")
	}
	if checkGauge(series, nil) == nil || checkGauge(nil, &zero) == nil {
		t.Fatal("null and zero conflated")
	}
}

func TestBindingsRejectPublicAddresses(t *testing.T) {
	for _, test := range []struct {
		data  string
		valid bool
	}{
		{`null`, true}, {`{"8080/tcp":[{"HostIp":"127.0.0.1"}]}`, true},
		{`{"8080/tcp":[{"HostIp":"0.0.0.0"}]}`, false}, {`{"8080/tcp":[{"HostIp":"::"}]}`, false},
	} {
		if (checkBindings([]byte(test.data)) == nil) != test.valid {
			t.Fatalf("unexpected binding result: %s", test.data)
		}
	}
}

func TestHTTPQueriesUseAuthenticationAndPreserveErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic synthetic" || r.Method != http.MethodPost {
			t.Error("query missing authentication or POST")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body["queries"].([]any)) != 1 {
			t.Error("missing query")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"results":{"A":{"error":"permission denied"}}}`))
	}))
	defer server.Close()
	client := stackClient{client: &http.Client{Timeout: time.Second}}
	if _, err := client.grafana(server.URL, "Basic synthetic", map[string]any{"refId": "A"}, false); err == nil {
		t.Fatal("HTTP query error accepted")
	}
	result, err := client.grafana(server.URL, "Basic synthetic", map[string]any{"refId": "A"}, true)
	if err != nil || result.Error != "permission denied" {
		t.Fatalf("expected SQL denial: %v", err)
	}
}
