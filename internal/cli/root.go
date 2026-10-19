// Package cli defines the command tree and connects commands to the tado client.
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/tado"
)

type application struct {
	credentialsFile string
}

// NewCommand returns a fresh command tree. Constructing it or requesting help
// does not read credentials, acquire locks or contact the provider.
func NewCommand(credentialsFile string) *cobra.Command {
	app := application{credentialsFile: credentialsFile}
	root := &cobra.Command{
		Use:           "tado-bridge",
		Short:         "Read tado homes and indoor-climate measurements",
		Long:          "Read tado homes and indoor-climate measurements.\n\nSet TADO_CREDENTIALS_FILE to a private credentials file. JSON and command results go to stdout; login instructions and request counts go to stderr.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          showHelp,
	}
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage local OAuth credentials",
		Args:  cobra.NoArgs,
		RunE:  showHelp,
	}
	auth.AddCommand(
		app.command("login", "Authorize access using the tado device-code flow", app.login),
		app.command("status", "Inspect local credentials without contacting tado", app.status),
		app.command("logout", "Remove local credentials without revoking upstream access", app.logout),
	)
	root.AddCommand(
		auth,
		app.command("discover", "Print homes and zones as filtered JSON", app.discover),
		app.command("read", "Collect measurements once and print a JSON snapshot", app.read),
		app.command("serve", "Collect periodically and serve a cached local JSON API", app.serve),
	)
	return root
}

func showHelp(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}

// All operational commands share the same credential lock and request accounting.
// Cobra validates arguments and handles help before this callback runs.
func (app application) command(use, short string, action func(*cobra.Command, *tado.Client) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if app.credentialsFile == "" {
				return errors.New("set TADO_CREDENTIALS_FILE to a private file path")
			}
			unlock, err := credentials.Lock(app.credentialsFile)
			if err != nil {
				return err
			}
			defer unlock()
			client := tado.New()
			defer func() {
				fmt.Fprintf(cmd.ErrOrStderr(), "Requests: api=%d oauth=%d\n", client.APIRequests, client.OAuthRequests)
			}()
			return action(cmd, client)
		},
	}
}
