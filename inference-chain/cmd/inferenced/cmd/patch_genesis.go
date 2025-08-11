package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/codec"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/genutil/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const flagGenTxDir = "gentx-dir"
const flagGenParticipantDir = "genparticipant-dir"

// PatchGenesisCmd - return the cobra command to patch genesis with genparticipant transactions
func PatchGenesisCmd(genBalIterator types.GenesisBalancesIterator, defaultNodeHome string, validator types.MessageValidator, valAddrCodec runtime.ValidatorAddressCodec) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "patch-genesis",
		Short: "Patch genesis.json with genparticipant transactions (MsgSubmitNewParticipant and authz grants)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			serverCtx := server.GetServerContextFromCmd(cmd)
			config := serverCtx.Config

			clientCtx := client.GetClientContextFromCmd(cmd)
			cdc := clientCtx.Codec

			config.SetRoot(clientCtx.HomeDir)

			// Load existing genesis file
			appGenesis, err := types.AppGenesisFromFile(config.GenesisFile())
			if err != nil {
				return errors.Wrap(err, "failed to read genesis doc from file")
			}

			// Get genparticipant directory
			genParticipantDir, _ := cmd.Flags().GetString(flagGenParticipantDir)
			genParticipantDirPath := genParticipantDir
			if genParticipantDirPath == "" {
				genParticipantDirPath = filepath.Join(config.RootDir, "config", "genparticipant")
			}

			// Collect genparticipant files
			genparticipantFiles, err := collectGenparticipantFiles(genParticipantDirPath)
			if err != nil {
				return errors.Wrap(err, "failed to collect genparticipant files")
			}

			if len(genparticipantFiles) == 0 {
				cmd.PrintErrf("No genparticipant files found in %q\n", genParticipantDirPath)
				return nil
			}

			// Process each genparticipant file
			var allTxs []sdk.Tx
			for _, file := range genparticipantFiles {
				cmd.PrintErrf("Processing genparticipant file: %s\n", file)

				// Read and decode the transaction
				tx, err := readGenparticipantFile(clientCtx, file)
				if err != nil {
					return errors.Wrapf(err, "failed to read genparticipant file %s", file)
				}

				// Verify the transaction messages
				msgs := tx.GetMsgs()
				for _, msg := range msgs {
					if m, ok := msg.(sdk.HasValidateBasic); ok {
						if err := m.ValidateBasic(); err != nil {
							return errors.Wrapf(err, "invalid message in genparticipant transaction file %s", file)
						}
					}
				}

				allTxs = append(allTxs, tx)
			}

			// Apply the transactions to genesis state
			if err := applyGenparticipantTxsToGenesis(cdc, appGenesis, allTxs); err != nil {
				return errors.Wrap(err, "failed to apply genparticipant transactions to genesis")
			}

			// Write the updated genesis file
			if err := appGenesis.SaveAs(config.GenesisFile()); err != nil {
				return errors.Wrap(err, "failed to write updated genesis file")
			}

			cmd.PrintErrf("Successfully patched genesis with %d genparticipant transactions\n", len(allTxs))
			cmd.PrintErrf("Updated genesis written to %q\n", config.GenesisFile())
			return nil
		},
	}

	cmd.Flags().String(flags.FlagHome, defaultNodeHome, "The application home directory")
	cmd.Flags().String(flagGenTxDir, "", "override default \"gentx\" directory from which collect and execute genesis transactions; default [--home]/config/gentx/")
	cmd.Flags().String(flagGenParticipantDir, "", "override default \"genparticipant\" directory from which collect genparticipant transactions; default [--home]/config/genparticipant/")

	return cmd
}

// collectGenparticipantFiles finds all genparticipant-*.json files in the specified directory
func collectGenparticipantFiles(genParticipantDir string) ([]string, error) {
	var genparticipantFiles []string

	// Check if directory exists
	if _, err := os.Stat(genParticipantDir); os.IsNotExist(err) {
		return genparticipantFiles, nil // Return empty slice if directory doesn't exist
	}

	// Read directory contents
	files, err := os.ReadDir(genParticipantDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", genParticipantDir, err)
	}

	// Filter for genparticipant files
	for _, file := range files {
		if !file.IsDir() && strings.HasPrefix(file.Name(), "genparticipant-") && strings.HasSuffix(file.Name(), ".json") {
			fullPath := filepath.Join(genParticipantDir, file.Name())
			genparticipantFiles = append(genparticipantFiles, fullPath)
		}
	}

	return genparticipantFiles, nil
}

// readGenparticipantFile reads and decodes a genparticipant transaction file
func readGenparticipantFile(clientCtx client.Context, filePath string) (sdk.Tx, error) {
	// Read the file
	bz, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	// Decode the transaction
	tx, err := clientCtx.TxConfig.TxJSONDecoder()(bz)
	if err != nil {
		return nil, fmt.Errorf("failed to decode transaction from file %s: %w", filePath, err)
	}

	return tx, nil
}

// applyGenparticipantTxsToGenesis applies the genparticipant transactions to the genesis state
func applyGenparticipantTxsToGenesis(cdc codec.Codec, appGenesis *types.AppGenesis, txs []sdk.Tx) error {
	// Unmarshal the current genesis state
	var genesisState map[string]json.RawMessage
	if err := json.Unmarshal(appGenesis.AppState, &genesisState); err != nil {
		return fmt.Errorf("failed to unmarshal genesis state: %w", err)
	}

	// Process each transaction
	for _, tx := range txs {
		msgs := tx.GetMsgs()
		for _, msg := range msgs {
			switch m := msg.(type) {
			case *inferencetypes.MsgSubmitNewParticipant:
				// Handle MsgSubmitNewParticipant - add to inference module state
				if err := addParticipantToGenesis(cdc, genesisState, m); err != nil {
					return fmt.Errorf("failed to add participant to genesis: %w", err)
				}
			case *authztypes.MsgGrant:
				// Handle MsgGrant - add to authz module state
				if err := addAuthzGrantToGenesis(cdc, genesisState, m); err != nil {
					return fmt.Errorf("failed to add authz grant to genesis: %w", err)
				}
			default:
				return fmt.Errorf("unexpected message type in genparticipant transaction: %T", msg)
			}
		}
	}

	// Marshal the updated genesis state back
	updatedAppState, err := json.Marshal(genesisState)
	if err != nil {
		return fmt.Errorf("failed to marshal updated genesis state: %w", err)
	}

	appGenesis.AppState = updatedAppState
	return nil
}

// addParticipantToGenesis adds a participant to the inference module genesis state
func addParticipantToGenesis(cdc codec.Codec, genesisState map[string]json.RawMessage, msg *inferencetypes.MsgSubmitNewParticipant) error {
	// This is a placeholder - you'll need to implement this based on your inference module's genesis structure
	// The exact implementation depends on how your inference module stores participants in genesis

	// Example structure (adjust based on your actual module):
	// 1. Get current inference genesis state
	// 2. Add the new participant to the participants list
	// 3. Update the inference genesis state in the overall genesis

	return fmt.Errorf("addParticipantToGenesis not yet implemented - needs inference module specific logic")
}

// addAuthzGrantToGenesis adds an authz grant to the authz module genesis state
func addAuthzGrantToGenesis(cdc codec.Codec, genesisState map[string]json.RawMessage, msg *authztypes.MsgGrant) error {
	// This is a placeholder - you'll need to implement this based on the authz module's genesis structure
	// The exact implementation depends on how the authz module stores grants in genesis

	// Example structure (adjust based on actual authz module):
	// 1. Get current authz genesis state
	// 2. Add the new grant to the grants list
	// 3. Update the authz genesis state in the overall genesis

	return fmt.Errorf("addAuthzGrantToGenesis not yet implemented - needs authz module specific logic")
}
