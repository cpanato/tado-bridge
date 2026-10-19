package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/tado"
)

func (app application) login(cmd *cobra.Command, client *tado.Client) error {
	token, err := client.Login(cmd.Context(), func(uri, code string) error {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Open this complete URL to authorize:\n%s\nIf prompted, enter code %s\n", uri, code)
		return err
	})
	if err != nil {
		return err
	}
	if err := credentials.Save(app.credentialsFile, token); err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.ErrOrStderr(), "Credentials saved.")
	return err
}

func (app application) status(cmd *cobra.Command, _ *tado.Client) error {
	token, err := credentials.Load(app.credentialsFile)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Local credentials present; access token expires at %s (expired: %t). Upstream validity not checked.\n", token.ExpiresAt.Format(time.RFC3339), !time.Now().Before(token.ExpiresAt))
	return err
}

func (app application) logout(cmd *cobra.Command, _ *tado.Client) error {
	if err := os.Remove(app.credentialsFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := fmt.Fprintln(cmd.ErrOrStderr(), "Local credentials removed; upstream authorization was not revoked.")
	return err
}

func (app application) token(ctx context.Context, client *tado.Client) (credentials.Token, error) {
	token, err := credentials.Load(app.credentialsFile)
	if err != nil {
		return credentials.Token{}, err
	}
	if time.Now().Add(30 * time.Second).Before(token.ExpiresAt) {
		return token, nil
	}
	token, err = client.Refresh(ctx, token)
	if err != nil {
		return credentials.Token{}, fmt.Errorf("refresh failed; authorization may need renewal: %w", err)
	}
	if err := credentials.Save(app.credentialsFile, token); err != nil {
		return credentials.Token{}, fmt.Errorf("cannot persist rotated credentials: %w", err)
	}
	return token, nil
}
