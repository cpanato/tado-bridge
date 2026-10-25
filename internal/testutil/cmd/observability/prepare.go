package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || owner.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return errors.New("state must be an owned, mode-0700 directory, not a symlink")
	}
	return nil
}

func privateWrite(path string, data []byte) error {
	// Check the opened descriptor before truncating. O_NONBLOCK also prevents
	// an unrelated FIFO from hanging preparation; O_NOFOLLOW rejects symlinks.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || owner.Uid != uint32(os.Getuid()) {
		return errors.New("generated files must be owned regular files")
	}
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err = file.Write(data)
	return err
}

func prepare(root, directory, token string) error {
	if token == "" || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
		return errors.New("API_TOKEN must contain printable ASCII without whitespace")
	}
	for _, dir := range []string{directory, filepath.Join(directory, "prometheus"), filepath.Join(directory, "grafana"), filepath.Join(directory, "dashboards")} {
		if err := privateDirectory(dir); err != nil {
			return err
		}
	}
	if err := mapDashboard(root, directory, "tado-bridge.json"); err != nil {
		return err
	}
	if err := mapDashboard(root, directory, "tado-bridge-summary.json"); err != nil {
		return err
	}
	return privateWrite(filepath.Join(directory, "api-token"), []byte(token))
}

func mapDashboard(root, directory, name string) error {
	data, err := os.ReadFile(filepath.Join(root, "integrations/grafana", name))
	if err != nil {
		return err
	}
	var dashboard map[string]json.RawMessage
	if err := json.Unmarshal(data, &dashboard); err != nil {
		return err
	}
	delete(dashboard, "__inputs")
	data, err = json.MarshalIndent(dashboard, "", "  ")
	if err != nil {
		return err
	}
	mapped := strings.NewReplacer("${DS_PROMETHEUS}", "tado-bridge-prometheus", "${DS_POSTGRESQL}", "tado-bridge-postgres").Replace(string(data))
	return privateWrite(filepath.Join(directory, "dashboards", name), []byte(mapped+"\n"))
}
