package cmd

import (
	"fmt"
	"github.com/productscience/inference/internal/rpc"
	"os"

	"github.com/spf13/cobra"
)

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
	genesis, err := rpc.DownloadGenesis(nodeAddress)
	if err != nil {
		return fmt.Errorf("failed to download genesis %w", err)
	}

	// FIXME: explain 0644
	if err := os.WriteFile(outputFile, genesis, 0644); err != nil {
		return fmt.Errorf("failed to write genesis file: %w", err)
	}
	return nil
}
