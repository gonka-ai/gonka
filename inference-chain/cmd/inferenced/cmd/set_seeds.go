package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"net/http"
)

type statusResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		Status json.RawMessage `json:"status"`
	}
}

func SetSeedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-seed [node-address]",
		Short: "Set seed to the node address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeAddress := args[0]

			err := setSeed(nodeAddress)
			if err != nil {
				return fmt.Errorf("Failed to set seed: %w", err)
			}

			fmt.Printf("Successfully set the seed to %s", nodeAddress)
			return nil
		},
	}
	return cmd
}

func setSeed(nodeAddress string) error {
	url := fmt.Sprintf("%s/status", nodeAddress)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-OK HTTP status: %s", resp.Status)
	}

	var genResp statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return fmt.Errorf("failed to decode genesis JSON: %w", err)
	}

	// TODO: whatever

	return nil
}
