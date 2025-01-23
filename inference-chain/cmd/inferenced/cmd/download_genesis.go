package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

type genesisResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		Genesis json.RawMessage `json:"genesis"`
	} `json:"result"`
}

// DownloadGenesisCommand returns the Cobra command for downloading a genesis file
func DownloadGenesisCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download-genesis [node-address] [output-file]",
		Short: "Download the genesis file from a remote Cosmos node and store only the JSON content of result.genesis locally",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeAddress := args[0]
			outputFile := args[1]

			err := downloadGenesis(nodeAddress, outputFile)
			if err != nil {
				return fmt.Errorf("failed to download genesis: %w", err)
			}

			fmt.Printf("Genesis file successfully downloaded from %s and saved to %s\n", nodeAddress, outputFile)
			return nil
		},
	}
	return cmd
}

func downloadGenesis(nodeAddress, outputFile string) error {
	url := fmt.Sprintf("%s/genesis", nodeAddress)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-OK HTTP status: %s", resp.Status)
	}

	var genResp genesisResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return fmt.Errorf("failed to decode genesis JSON: %w", err)
	}

	// FIXME: explain 0644
	if err := os.WriteFile(outputFile, genResp.Result.Genesis, 0644); err != nil {
		return fmt.Errorf("failed to write genesis file: %w", err)
	}

	return nil
}
