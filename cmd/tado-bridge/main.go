package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/tado"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv("TADO_CREDENTIALS_FILE"), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, path string, stdout, stderr io.Writer) error {
	command := ""
	if len(args) == 1 && (args[0] == "discover" || args[0] == "read") {
		command = args[0]
	}
	if len(args) == 2 && args[0] == "auth" && (args[1] == "login" || args[1] == "status" || args[1] == "logout") {
		command = args[1]
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(stdout, "Usage: tado-bridge auth {login|status|logout} | discover | read\nSet TADO_CREDENTIALS_FILE to a private file path. Discovery or one-shot measurement JSON goes to stdout; login instructions and request counts go to stderr.")
		return err
	}
	if command == "" {
		return errors.New("usage: tado-bridge auth {login|status|logout} | discover | read")
	}
	if path == "" {
		return errors.New("set TADO_CREDENTIALS_FILE to a private file path")
	}
	unlock, err := credentials.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	client := tado.New()
	defer func() { fmt.Fprintf(stderr, "Requests: api=%d oauth=%d\n", client.APIRequests, client.OAuthRequests) }()
	switch command {
	case "login":
		token, err := client.Login(ctx, func(uri, code string) error {
			_, err := fmt.Fprintf(stderr, "Open this complete URL to authorize:\n%s\nIf prompted, enter code %s\n", uri, code)
			return err
		})
		if err != nil {
			return err
		}
		if err := credentials.Save(path, token); err != nil {
			return err
		}
		_, err = fmt.Fprintln(stderr, "Credentials saved.")
		return err
	case "logout":
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err := fmt.Fprintln(stderr, "Local credentials removed; upstream authorization was not revoked.")
		return err
	}
	token, err := credentials.Load(path)
	if err != nil {
		return err
	}
	if command == "status" {
		_, err := fmt.Fprintf(stdout, "Local credentials present; access token expires at %s (expired: %t). Upstream validity not checked.\n", token.ExpiresAt.Format(time.RFC3339), !time.Now().Before(token.ExpiresAt))
		return err
	}
	if !time.Now().Add(30 * time.Second).Before(token.ExpiresAt) {
		token, err = client.Refresh(ctx, token)
		if err != nil {
			return fmt.Errorf("refresh failed; authorization may need renewal: %w", err)
		}
		if err := credentials.Save(path, token); err != nil {
			return fmt.Errorf("cannot persist rotated credentials: %w", err)
		}
	}
	var result any
	if command == "read" {
		result, err = client.Read(ctx, token)
	} else {
		result, err = client.Discover(ctx, token)
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
