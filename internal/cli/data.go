package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/cpanato/tado-bridge/internal/tado"
)

func (app application) discover(cmd *cobra.Command, client *tado.Client) error {
	token, err := app.token(cmd.Context(), client)
	if err != nil {
		return err
	}
	result, err := client.Discover(cmd.Context(), token)
	if err != nil {
		return err
	}
	return writeJSON(cmd, result)
}

func (app application) read(cmd *cobra.Command, client *tado.Client) error {
	token, err := app.token(cmd.Context(), client)
	if err != nil {
		return err
	}
	result, err := client.Read(cmd.Context(), token)
	if err != nil {
		return err
	}
	return writeJSON(cmd, result)
}

func writeJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
