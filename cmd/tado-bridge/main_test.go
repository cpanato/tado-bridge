package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cpanato/tado-bridge/internal/credentials"
)

func TestLocalCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	token := credentials.Token{AccessToken: "secret-access", RefreshToken: "secret-refresh", ExpiresAt: time.Now().Add(time.Hour).UTC()}
	if err := credentials.Save(path, token); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err := run(context.Background(), []string{"auth", "status"}, path, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+diagnostics.String(), "secret-") || !strings.Contains(diagnostics.String(), "api=0 oauth=0") {
		t.Fatal("unsafe status or unexpected network calls")
	}
	for range 2 {
		if err := run(context.Background(), []string{"auth", "logout"}, path, &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := credentials.Load(path); err == nil {
		t.Fatal("logout did not remove credentials")
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}, {"auth", "login", "extra"}, {"discover"}, {"read"}, {"read", "extra"}} {
		if err := run(context.Background(), args, "", &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
	if err := run(context.Background(), []string{"--help"}, "", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
