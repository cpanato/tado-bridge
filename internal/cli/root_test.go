package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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
	for _, args := range [][]string{{"serve"}, {"auth", "login", "extra"}, {"discover"}, {"read"}, {"read", "extra"}} {
		if err := run(context.Background(), args, "", &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
	if err := run(context.Background(), []string{"--help"}, "", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func run(ctx context.Context, args []string, path string, stdout, stderr io.Writer) error {
	command := NewCommand(path)
	command.SetArgs(append([]string{}, args...))
	command.SetOut(stdout)
	command.SetErr(stderr)
	return command.ExecuteContext(ctx)
}

func TestHelpDoesNotTouchCredentials(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"auth"}, {"auth", "--help"}, {"auth", "login", "--help"}, {"auth", "status", "-h"}, {"auth", "logout", "--help"}, {"discover", "--help"}, {"read", "--help"}, {"help", "auth", "login"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "not-created", "credentials.json")
			var out, diagnostics bytes.Buffer
			if err := run(context.Background(), args, path, &out, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Usage:") || diagnostics.Len() != 0 {
				t.Fatalf("help streams: stdout=%q stderr=%q", out.String(), diagnostics.String())
			}
			if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("help touched credential directory")
			}
		})
	}
}

func TestInvalidArgumentsHaveNoSideEffects(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"auth", "unknown"}, {"auth", "status", "extra"}, {"auth", "logout", "extra"}, {"auth", "login", "extra"}, {"discover", "extra"}, {"read", "extra"}, {"read", "--unknown"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			token := credentials.Token{AccessToken: "secret-access", RefreshToken: "secret-refresh", ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second)}
			if err := credentials.Save(path, token); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			if err := run(context.Background(), args, path, &out, &diagnostics); err == nil {
				t.Fatal("expected argument error")
			}
			if out.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatal("Cobra should return errors for main to print once, without running commands")
			}
			actual, err := credentials.Load(path)
			if err != nil || actual != token {
				t.Fatal("invalid arguments altered credentials")
			}
			if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid arguments left a lock")
			}
		})
	}
}

func TestOperationalCommandsRequireCredentialsPath(t *testing.T) {
	for _, args := range [][]string{{"auth", "login"}, {"auth", "status"}, {"auth", "logout"}, {"discover"}, {"read"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			err := run(context.Background(), args, "", &out, &diagnostics)
			if err == nil || !strings.Contains(err.Error(), "TADO_CREDENTIALS_FILE") {
				t.Fatalf("expected configuration error, got %v", err)
			}
			if out.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatal("missing configuration must not run an operation")
			}
		})
	}
}
