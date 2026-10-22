package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type commandRunner func(string, map[string]string, []string, bool) ([]byte, error)

type previewState struct {
	Purpose         string            `json:"purpose"`
	Project         string            `json:"project"`
	Environment     map[string]string `json:"environment"`
	GrafanaURL      string            `json:"grafana_url"`
	GrafanaUser     string            `json:"grafana_user"`
	GrafanaPassword string            `json:"grafana_password"`
}

func execute(root string, values map[string]string, args []string, capture bool) ([]byte, error) {
	return executeContext(context.Background(), root, values, args, capture)
}

func executeContext(ctx context.Context, root string, values map[string]string, args []string, capture bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	for key, value := range values {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stderr = os.Stderr
	if capture {
		return cmd.Output()
	}
	cmd.Stdout = os.Stdout
	return nil, cmd.Run()
}

func composeArgs(root, directory, project string, args ...string) []string {
	command := []string{"docker", "compose", "--project-name", project, "--env-file", "/dev/null"}
	for _, file := range []string{"compose.yaml", "compose.history.yaml", "compose.observability.yaml"} {
		command = append(command, "-f", filepath.Join(root, file))
	}
	return append(append(command, "-f", filepath.Join(directory, "preview.json")), args...)
}

func validateState(directory string, state previewState) error {
	if state.Purpose != purpose || state.Project != filepath.Base(directory) || !strings.HasPrefix(state.Project, "tado-observability-test-") {
		return errors.New("not a retained synthetic preview")
	}
	return nil
}

func stop(root, stateFile string, run commandRunner) error {
	stateFile, err := filepath.Abs(stateFile)
	if err != nil {
		return err
	}
	directory := filepath.Dir(stateFile)
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return err
	}
	var state previewState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if err := validateState(directory, state); err != nil {
		return err
	}
	if _, err := run(root, state.Environment, composeArgs(root, directory, state.Project, "down", "--volumes", "--timeout", "15"), false); err != nil {
		return err
	}
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	fmt.Println("Synthetic preview stopped and its temporary state removed.")
	return nil
}

func freePort() (string, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = listener.Close() }()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), nil
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return privateWrite(path, data)
}

func smoke(root string, keep bool, screenshot string) (result error) {
	if os.Getuid() == 0 {
		return errors.New("run as a nonroot host user")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	directory, err := os.MkdirTemp("", "tado-observability-test-")
	if err != nil {
		return err
	}
	project := filepath.Base(directory)
	values := map[string]string{
		"TADO_BRIDGE_UID": strconv.Itoa(os.Getuid()), "TADO_BRIDGE_GID": strconv.Itoa(os.Getgid()),
		"TADO_CREDENTIALS_DIR": filepath.Join(directory, "credentials"), "TADO_BRIDGE_IMAGE": "ko.local:fixture",
		"OBSERVABILITY_DIR": filepath.Join(directory, "state"), "HISTORY_DATABASE_URL": "", "HISTORY_RETENTION": "2160h",
		"METRICS_ENABLED": "true", "METRICS_ROOM_DETAILS": "true", "TRMNL_ENABLED": "false", "TRMNL_WEBHOOK_URL": "", "TRMNL_ROOMS": "",
		"POLL_INTERVAL": "30m", "MAX_SNAPSHOT_AGE": "2h", "TADO_DAILY_REQUEST_BUDGET": "90",
	}
	retained := false
	configured := false
	compose := func(args ...string) ([]byte, error) {
		return executeContext(ctx, root, values, composeArgs(root, directory, project, args...), false)
	}
	defer func() {
		if retained {
			return
		}
		if configured && result != nil {
			_, _ = execute(root, values, composeArgs(root, directory, project, "logs", "--tail", "30"), false)
		}
		if configured {
			if _, err := execute(root, values, composeArgs(root, directory, project, "down", "--volumes", "--timeout", "15"), false); err != nil {
				// Preserve private state if Docker could not clean up; report failure even
				// when the assertions passed so CI cannot silently leak a test stack.
				if result == nil {
					result = err
				}
				fmt.Fprintln(os.Stderr, "Cleanup failed; temporary state retained at", directory)
				return
			}
		}
		if err := os.RemoveAll(directory); result == nil {
			result = err
		}
	}()
	for _, key := range []string{"TADO_BRIDGE_PORT", "PROMETHEUS_PORT", "GRAFANA_PORT"} {
		values[key], err = freePort()
		if err != nil {
			return err
		}
	}
	for _, key := range []string{"API_TOKEN", "HISTORY_POSTGRES_PASSWORD", "GRAFANA_ADMIN_PASSWORD", "GRAFANA_DB_PASSWORD"} {
		values[key] = rand.Text()
	}
	if err := os.Mkdir(values["TADO_CREDENTIALS_DIR"], 0o700); err != nil {
		return err
	}
	override := map[string]any{"services": map[string]any{"tado-bridge": map[string]any{
		"command": []string{"preview"}, "healthcheck": map[string]any{"test": []string{"CMD", "/ko-app/tadofake", "preview-healthcheck"}, "interval": "1s"},
	}}}
	if err := writeJSON(filepath.Join(directory, "preview.json"), override); err != nil {
		return err
	}
	if err := prepare(root, values["OBSERVABILITY_DIR"], values["API_TOKEN"]); err != nil {
		return err
	}
	if _, err := compose("config", "--quiet"); err != nil {
		return err
	}
	configured = true
	if _, err := compose("up", "--detach", "--wait", "--wait-timeout", "180"); err != nil {
		return err
	}
	containers, err := executeContext(ctx, root, values, composeArgs(root, directory, project, "ps", "--all", "--quiet"), true)
	if err != nil {
		return err
	}
	for _, container := range strings.Fields(string(containers)) {
		bindings, err := executeContext(ctx, root, values, []string{"docker", "inspect", "--format", "{{json .HostConfig.PortBindings}}", container}, true)
		if err != nil {
			return err
		}
		if err := checkBindings(bindings); err != nil {
			return err
		}
	}
	grafana := "http://127.0.0.1:" + values["GRAFANA_PORT"]
	if err := checkStack(ctx, values); err != nil {
		return err
	}
	if _, err := compose("exec", "-T", "postgres", "sh", "/docker-entrypoint-initdb.d/20-grafana-reader.sh"); err != nil {
		return err
	}
	if screenshot != "" {
		config := filepath.Join(directory, "capture.json")
		if err := writeJSON(config, map[string]string{"purpose": purpose, "url": grafana, "password": values["GRAFANA_ADMIN_PASSWORD"]}); err != nil {
			return err
		}
		node := os.Getenv("NODE")
		if node == "" {
			node = "node"
		}
		screenshot, err = filepath.Abs(screenshot)
		if err != nil {
			return err
		}
		if _, err := executeContext(ctx, root, nil, []string{node, filepath.Join(root, "scripts/capture-grafana.mjs"), config, screenshot}, false); err != nil {
			return err
		}
	}
	fmt.Println("Optional Compose: 11 Grafana queries, eight discovered rooms, nullable room gauges, authenticated scrapes and read-only history passed.")
	if keep {
		stateFile := filepath.Join(directory, "preview-state.json")
		state := previewState{purpose, project, values, grafana + "/d/tado-bridge", "admin", values["GRAFANA_ADMIN_PASSWORD"]}
		if err := writeJSON(stateFile, state); err != nil {
			return err
		}
		retained = true
		fmt.Println("Grafana preview:", state.GrafanaURL)
		fmt.Println("Private login and cleanup state:", stateFile)
		fmt.Println("Stop with: go run ./internal/testutil/cmd/observability stop", stateFile)
	}
	return nil
}

func checkBindings(data []byte) error {
	var bindings map[string][]struct {
		HostIP string `json:"HostIp"`
	}
	if err := json.Unmarshal(data, &bindings); err != nil {
		return err
	}
	for _, addresses := range bindings {
		for _, address := range addresses {
			if address.HostIP != "127.0.0.1" {
				return errors.New("test service is publicly bound")
			}
		}
	}
	return nil
}
