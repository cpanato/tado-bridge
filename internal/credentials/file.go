// Package credentials stores OAuth credentials locally; never print these values.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func Load(path string) (Token, error) {
	var token Token
	info, err := os.Lstat(path)
	if err != nil {
		return token, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return token, errors.New("credentials must be a regular file accessible only by its owner (chmod 600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return token, err
	}
	if json.Unmarshal(data, &token) != nil {
		return token, errors.New("invalid credentials JSON")
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresAt.IsZero() {
		return token, errors.New("incomplete credentials; run auth login")
	}
	return token, nil
}

func Save(path string, token Token) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tado-token-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(token); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return nil
}

// Lock serializes CLI processes so refresh-token rotation cannot race.
// A process killed without cleanup leaves a lock directory for manual removal.
func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		return nil, fmt.Errorf("credentials locked or inaccessible; if no command is running, remove %s.lock: %w", path, err)
	}
	return func() { _ = os.Remove(path + ".lock") }, nil
}
