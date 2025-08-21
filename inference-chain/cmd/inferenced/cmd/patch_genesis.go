package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/codec"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
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
				tx, err := readGenparticipantFile(clientCtx, appGenesis.ChainID, file)
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
func readGenparticipantFile(clientCtx client.Context, chainID string, filePath string) (sdk.Tx, error) {
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

	// Verify transaction signatures
	if err := verifyTransactionSignatures(clientCtx, chainID, tx); err != nil {
		return nil, fmt.Errorf("signature verification failed for file %s: %w", filePath, err)
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

	// Collect unique ML operational addresses that need accounts
	mlOperationalAddresses := make(map[string]bool)

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
				// Collect ML operational addresses for account creation
				mlOperationalAddresses[m.Grantee] = true
				// Handle MsgGrant - add to authz module state
				if err := addAuthzGrantToGenesis(cdc, genesisState, m); err != nil {
					return fmt.Errorf("failed to add authz grant to genesis: %w", err)
				}
			default:
				return fmt.Errorf("unexpected message type in genparticipant transaction: %T", msg)
			}
		}
	}

	// Add ML operational key accounts to auth module accounts section
	for mlAddress := range mlOperationalAddresses {
		if err := addAccountToGenesis(cdc, genesisState, mlAddress); err != nil {
			return fmt.Errorf("failed to add ML operational account %s to genesis: %w", mlAddress, err)
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
	// Local view of inference genesis state that includes participant list field,
	// which is present in our chain genesis JSON.
	type inferenceGenesisState struct {
		Params            json.RawMessage              `json:"params"`
		GenesisOnlyParams json.RawMessage              `json:"genesis_only_params"`
		ModelList         json.RawMessage              `json:"model_list"`
		Bridge            json.RawMessage              `json:"bridge"`
		ParticipantList   []inferencetypes.Participant `json:"participant_list"`
	}

	// Fetch existing module state
	modBz, ok := genesisState["inference"]
	if !ok {
		return fmt.Errorf("inference module state not found in genesis")
	}

	var infGS inferenceGenesisState
	if err := json.Unmarshal(modBz, &infGS); err != nil {
		return fmt.Errorf("failed to unmarshal inference genesis: %w", err)
	}

	// Convert message into a Participant entry for genesis
	newP := inferencetypes.Participant{
		Index:             msg.GetCreator(),
		Address:           msg.GetCreator(),
		Weight:            1,
		JoinTime:          time.Now().Unix(),
		JoinHeight:        0,
		LastInferenceTime: time.Now().Unix(),
		InferenceUrl:      msg.GetUrl(),
		Status:            inferencetypes.ParticipantStatus_ACTIVE,
		CoinBalance:       0,
		ValidatorKey:      msg.GetValidatorKey(),
		WorkerPublicKey:   msg.GetWorkerKey(),
		EpochsCompleted:   0,
		CurrentEpochStats: &inferencetypes.CurrentEpochStats{},
	}

	// Upsert by index (bech32 account address)
	replaced := false
	for i := range infGS.ParticipantList {
		if infGS.ParticipantList[i].Index == newP.Index {
			infGS.ParticipantList[i] = newP
			replaced = true
			break
		}
	}
	if !replaced {
		infGS.ParticipantList = append(infGS.ParticipantList, newP)
	}

	// Marshal back and place into app state
	updatedBz, err := json.Marshal(infGS)
	if err != nil {
		return fmt.Errorf("failed to marshal updated inference genesis: %w", err)
	}

	genesisState["inference"] = updatedBz
	return nil
}

// addAuthzGrantToGenesis adds an authz grant to the authz module genesis state
func addAuthzGrantToGenesis(cdc codec.Codec, genesisState map[string]json.RawMessage, msg *authztypes.MsgGrant) error {
	// Represent authz genesis as plain JSON (avoid marshaling protobuf Any via encoding/json).
	type jsonGrant struct {
		Granter       string         `json:"granter"`
		Grantee       string         `json:"grantee"`
		Authorization map[string]any `json:"authorization"`
		Expiration    string         `json:"expiration,omitempty"`
	}
	type authzGenesisState struct {
		Authorization []jsonGrant `json:"authorization"`
	}

	// Fetch existing authz state or init
	var azGS authzGenesisState
	if modBz, ok := genesisState["authz"]; ok {
		if err := json.Unmarshal(modBz, &azGS); err != nil {
			return fmt.Errorf("failed to unmarshal authz genesis: %w", err)
		}
	}

	// Unpack GenericAuthorization to get msg type, if possible
	// Default to GenericAuthorization with msg set to the embedded Any.TypeUrl when unpack fails
	authMap := map[string]any{
		"@type": "/cosmos.authz.v1beta1.GenericAuthorization",
	}
	if msg.Grant.Authorization != nil {
		// Fallback: use embedded Any.TypeUrl for inner message type
		if msg.Grant.Authorization.TypeUrl != "" {
			authMap["msg"] = msg.Grant.Authorization.TypeUrl
		}
	}
	// Try to extract inner msg without needing the codec by directly unmarshaling Any.Value
	if msg.Grant.Authorization != nil && len(msg.Grant.Authorization.Value) > 0 {
		var gen authztypes.GenericAuthorization
		if err := gen.Unmarshal(msg.Grant.Authorization.Value); err == nil && gen.Msg != "" {
			authMap["msg"] = gen.Msg
		}
	}
	// If codec provided, also attempt interface unpack (no harm if already set)
	if msg.Grant.Authorization != nil && cdc != nil {
		var gen authztypes.GenericAuthorization
		if err := cdc.UnpackAny(msg.Grant.Authorization, &gen); err == nil && gen.Msg != "" {
			authMap["msg"] = gen.Msg
		}
	}

	// Format expiration to RFC3339 if present
	expStr := ""
	if msg.Grant.Expiration != nil {
		expStr = msg.Grant.Expiration.UTC().Format(time.RFC3339)
	}

	newGA := jsonGrant{
		Granter:       msg.Granter,
		Grantee:       msg.Grantee,
		Authorization: authMap,
		Expiration:    expStr,
	}

	// Deduplicate by (granter, grantee, type_url, msg)
	newType := authMap["@type"]
	newMsg, _ := authMap["msg"].(string)
	replaced := false
	for i := range azGS.Authorization {
		exist := azGS.Authorization[i]
		existType := exist.Authorization["@type"]
		existMsg, _ := exist.Authorization["msg"].(string)
		if exist.Granter == newGA.Granter && exist.Grantee == newGA.Grantee && existType == newType && existMsg == newMsg {
			azGS.Authorization[i] = newGA
			replaced = true
			break
		}
	}
	if !replaced {
		azGS.Authorization = append(azGS.Authorization, newGA)
	}

	// Marshal back and place into app state
	updatedBz, err := json.Marshal(azGS)
	if err != nil {
		return fmt.Errorf("failed to marshal updated authz genesis: %w", err)
	}
	genesisState["authz"] = updatedBz
	return nil
}

// addAccountToGenesis adds an ML operational key account to the auth module accounts section
func addAccountToGenesis(cdc codec.Codec, genesisState map[string]json.RawMessage, accountAddress string) error {
	// Validate the account address format
	_, err := sdk.AccAddressFromBech32(accountAddress)
	if err != nil {
		return fmt.Errorf("invalid account address format: %s", accountAddress)
	}

	// Local view of auth genesis state
	type authGenesisState struct {
		Params   json.RawMessage   `json:"params"`
		Accounts []json.RawMessage `json:"accounts"`
	}

	// Fetch existing auth state
	modBz, ok := genesisState["auth"]
	if !ok {
		return fmt.Errorf("auth module state not found in genesis")
	}

	var authGS authGenesisState
	if err := json.Unmarshal(modBz, &authGS); err != nil {
		return fmt.Errorf("failed to unmarshal auth genesis: %w", err)
	}

	// Check if account already exists
	for _, accountRaw := range authGS.Accounts {
		var account map[string]interface{}
		if err := json.Unmarshal(accountRaw, &account); err != nil {
			continue // Skip malformed accounts
		}
		if existingAddress, ok := account["address"].(string); ok && existingAddress == accountAddress {
			// Account already exists, skip creation
			return nil
		}
	}

	// Find the next available account number
	nextAccountNumber := len(authGS.Accounts)

	// Create new BaseAccount for ML operational key
	newAccount := map[string]interface{}{
		"@type":          "/cosmos.auth.v1beta1.BaseAccount",
		"address":        accountAddress,
		"pub_key":        nil,
		"account_number": fmt.Sprintf("%d", nextAccountNumber),
		"sequence":       "0",
	}

	// Marshal the new account
	newAccountBytes, err := json.Marshal(newAccount)
	if err != nil {
		return fmt.Errorf("failed to marshal new account: %w", err)
	}

	// Add to accounts list
	authGS.Accounts = append(authGS.Accounts, newAccountBytes)

	// Marshal back and place into app state
	updatedBz, err := json.Marshal(authGS)
	if err != nil {
		return fmt.Errorf("failed to marshal updated auth genesis: %w", err)
	}

	genesisState["auth"] = updatedBz
	return nil
}

// verifyTransactionSignatures verifies the signatures of a transaction using real cryptographic verification
func verifyTransactionSignatures(clientCtx client.Context, chainID string, tx sdk.Tx) error {
	// Ensure crypto types are registered for pubkey unpacking
	if clientCtx.InterfaceRegistry != nil {
		cryptocodec.RegisterInterfaces(clientCtx.InterfaceRegistry)
	}

	// Encode the transaction to JSON and then decode back to get the protobuf structure
	txBytes, err := clientCtx.TxConfig.TxJSONEncoder()(tx)
	if err != nil {
		return fmt.Errorf("failed to encode transaction to JSON: %w", err)
	}

	// Decode the JSON back to an sdk.Tx to ensure we have a proper transaction
	decodedTx, err := clientCtx.TxConfig.TxJSONDecoder()(txBytes)
	if err != nil {
		return fmt.Errorf("failed to decode transaction from JSON: %w", err)
	}

	// Now encode to protobuf bytes to get the raw transaction
	protoBytes, err := clientCtx.TxConfig.TxEncoder()(decodedTx)
	if err != nil {
		return fmt.Errorf("failed to encode transaction to protobuf: %w", err)
	}

	// Unmarshal the protobuf to get the transaction structure
	var txProto txtypes.Tx
	if err := clientCtx.Codec.Unmarshal(protoBytes, &txProto); err != nil {
		return fmt.Errorf("failed to unmarshal transaction: %w", err)
	}

	if txProto.AuthInfo == nil || txProto.Body == nil {
		return fmt.Errorf("transaction missing body or authinfo")
	}
	if len(txProto.Signatures) == 0 {
		return fmt.Errorf("no signatures found in transaction")
	}
	if len(txProto.AuthInfo.SignerInfos) != len(txProto.Signatures) {
		return fmt.Errorf("mismatch between number of signers (%d) and signatures (%d)",
			len(txProto.AuthInfo.SignerInfos), len(txProto.Signatures))
	}

	// Recreate sign bytes
	bodyBytes, err := clientCtx.Codec.Marshal(txProto.Body)
	if err != nil {
		return fmt.Errorf("failed to marshal tx body: %w", err)
	}
	authInfoBytes, err := clientCtx.Codec.Marshal(txProto.AuthInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal auth info: %w", err)
	}
	signDoc := txtypes.SignDoc{BodyBytes: bodyBytes, AuthInfoBytes: authInfoBytes, ChainId: chainID, AccountNumber: 0}
	signBytes, err := signDoc.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal sign doc: %w", err)
	}

	// Verify each signature
	for i, signerInfo := range txProto.AuthInfo.SignerInfos {
		signature := txProto.Signatures[i]
		if signerInfo.PublicKey == nil {
			return fmt.Errorf("signer %d has no public key", i)
		}
		var pubKey cryptotypes.PubKey
		if err := clientCtx.Codec.UnpackAny(signerInfo.PublicKey, &pubKey); err != nil {
			return fmt.Errorf("failed to unpack public key for signer %d: %w", i, err)
		}
		if !pubKey.VerifySignature(signBytes, signature) {
			return fmt.Errorf("signature verification failed for signer %d", i)
		}
	}

	fmt.Printf("Transaction signature verification passed for %d signatures, tx size: %d bytes\n",
		len(txProto.Signatures), len(txBytes))
	return nil
}
