package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	address "cosmossdk.io/core/address"
	"cosmossdk.io/errors"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/server"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/version"
	authclient "github.com/cosmos/cosmos-sdk/x/auth/client"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	"github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/cosmos/cosmos-sdk/x/staking/client/cli"
	"github.com/productscience/inference/api/inference/inference"
	inferencepkg "github.com/productscience/inference/x/inference"
	"github.com/productscience/inference/x/inference/utils"
)

// GenTxCmd builds the application's gentx command.
func GenTxCmd(mbm module.BasicManager, txEncCfg client.TxEncodingConfig, genBalIterator types.GenesisBalancesIterator, defaultNodeHome string, valAdddressCodec address.Codec) *cobra.Command {
	ipDefault, _ := server.ExternalIP()
	fsCreateValidator, defaultsDesc := cli.CreateValidatorMsgFlagSet(ipDefault)

	cmd := &cobra.Command{
		Use:   "gentx [key_name] [amount] --pubkey [base64_consensus_key] --ml-operational-address [ml_operational_address] --url [url]",
		Short: "[CUSTOM] Generate two genesis transactions: classic gentx and genparticipant",
		Args:  cobra.ExactArgs(2),
		Long: fmt.Sprintf(`[CUSTOM] Generate two genesis transactions:
1. Classic gentx (config/gentx/gentx-[nodeID].json): Contains MsgCreateValidator
2. Genparticipant (config/genparticipant/genparticipant-[nodeID].json): Contains MsgSubmitNewParticipant and authz grants

Both transactions are signed by the Account Key (cold key) in the Keyring referenced by a given name. A BASE64-encoded ED25519 consensus key and ML Operational address (warm key) are required.
The following default parameters are included:
    %s

Example:
$ %s gentx my-key-name 1000000nicoin --pubkey x+OH2yt/GC/zK/fR5ImKnlfrmE6nZO/11FKXOpWRmAA= --ml-operational-address gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm
`, defaultsDesc, version.AppName,
		),
		RunE: func(cmd *cobra.Command, args []string) error {
			serverCtx := server.GetServerContextFromCmd(cmd)
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			cdc := clientCtx.Codec

			config := serverCtx.Config
			config.SetRoot(clientCtx.HomeDir)

			nodeID, valPubKey, err := genutil.InitializeNodeValidatorFiles(serverCtx.Config)
			if err != nil {
				return errors.Wrap(err, "failed to initialize node validator files")
			}

			// read --nodeID, if empty take it from priv_validator.json
			if nodeIDString, _ := cmd.Flags().GetString(cli.FlagNodeID); nodeIDString != "" {
				nodeID = nodeIDString
			}

			// read and validate --pubkey (BASE64 format), if empty take it from priv_validator.json
			if pkStr, _ := cmd.Flags().GetString(cli.FlagPubKey); pkStr != "" {
				// Validate the BASE64 consensus key using SafeCreateED25519ValidatorKey
				validatedPubKey, err := utils.SafeCreateED25519ValidatorKey(pkStr)
				if err != nil {
					return errors.Wrapf(err, "invalid consensus key format: %s", pkStr)
				}

				// The validated key is already in the correct format for validator creation
				valPubKey = validatedPubKey
			}

			// read and validate --ml-operational-address (required)
			mlOperationalAddressStr, err := cmd.Flags().GetString("ml-operational-address")
			if err != nil {
				return errors.Wrap(err, "failed to get ml-operational-address flag")
			}
			if mlOperationalAddressStr == "" {
				return fmt.Errorf("ml-operational-address flag is required")
			}

			// Validate ML operational address format (must be valid Bech32 account address)
			mlOperationalAddress, err := sdk.AccAddressFromBech32(mlOperationalAddressStr)
			if err != nil {
				return errors.Wrapf(err, "invalid ML operational address format: %s", mlOperationalAddressStr)
			}

			// Additional validation: ensure it's not empty after parsing
			if len(mlOperationalAddress) == 0 {
				return fmt.Errorf("ML operational address cannot be empty")
			}

			urlStr, err := cmd.Flags().GetString("url")
			if err != nil {
				return errors.Wrap(err, "failed to get url flag")
			} else if urlStr == "" {
				return fmt.Errorf("url flag is required")
			}

			appGenesis, err := types.AppGenesisFromFile(config.GenesisFile())
			if err != nil {
				return errors.Wrapf(err, "failed to read genesis doc file %s", config.GenesisFile())
			}

			var genesisState map[string]json.RawMessage
			if err = json.Unmarshal(appGenesis.AppState, &genesisState); err != nil {
				return errors.Wrap(err, "failed to unmarshal genesis state")
			}

			if err = mbm.ValidateGenesis(cdc, txEncCfg, genesisState); err != nil {
				return errors.Wrap(err, "failed to validate genesis state")
			}

			inBuf := bufio.NewReader(cmd.InOrStdin())

			name := args[0]
			key, err := clientCtx.Keyring.Key(name)
			if err != nil {
				return errors.Wrapf(err, "failed to fetch '%s' from the keyring", name)
			}

			moniker := config.Moniker
			if m, _ := cmd.Flags().GetString(cli.FlagMoniker); m != "" {
				moniker = m
			}

			// set flags for creating a gentx
			createValCfg, err := cli.PrepareConfigForTxCreateValidator(cmd.Flags(), moniker, nodeID, appGenesis.ChainID, valPubKey)
			if err != nil {
				return errors.Wrap(err, "error creating configuration to create validator msg")
			}

			amount := args[1]
			coins, err := sdk.ParseCoinsNormalized(amount)
			if err != nil {
				return errors.Wrap(err, "failed to parse coins")
			}
			addr, err := key.GetAddress()
			if err != nil {
				return err
			}
			err = genutil.ValidateAccountInGenesis(genesisState, genBalIterator, addr, coins, cdc)
			if err != nil {
				return errors.Wrap(err, "failed to validate account in genesis")
			}

			txFactory, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}

			pub, err := key.GetAddress()
			if err != nil {
				return err
			}
			clientCtx = clientCtx.WithInput(inBuf).WithFromAddress(pub)

			// The following line comes from a discrepancy between the `gentx`
			// and `create-validator` commands:
			// - `gentx` expects amount as an arg,
			// - `create-validator` expects amount as a required flag.
			// ref: https://github.com/cosmos/cosmos-sdk/issues/8251
			// Since gentx doesn't set the amount flag (which `create-validator`
			// reads from), we copy the amount arg into the valCfg directly.
			//
			// Ideally, the `create-validator` command should take a validator
			// config file instead of so many flags.
			// ref: https://github.com/cosmos/cosmos-sdk/issues/8177
			createValCfg.Amount = amount

			// === FIRST FILE: Classic gentx with MsgCreateValidator only ===
			classicMessages := make([]sdk.Msg, 0)

			// create a 'create-validator' message
			_, createValidatorMsg, err := cli.BuildCreateValidatorMsg(clientCtx, createValCfg, txFactory, true, valAdddressCodec)
			if err != nil {
				return errors.Wrap(err, "failed to build create-validator message")
			}
			classicMessages = append(classicMessages, createValidatorMsg)

			// Create and sign the classic gentx
			classicTx, err := createAndSignTx(clientCtx, txFactory, name, classicMessages)
			if err != nil {
				return errors.Wrap(err, "failed to create classic gentx")
			}

			// Write classic gentx file
			classicOutputDocument, err := makeClassicOutputFilepath(config.RootDir, nodeID)
			if err != nil {
				return errors.Wrap(err, "failed to create classic output file path")
			}

			if err := writeSignedGenTx(clientCtx, classicOutputDocument, classicTx); err != nil {
				return errors.Wrap(err, "failed to write classic gentx")
			}

			cmd.PrintErrf("Classic genesis transaction written to %q\n", classicOutputDocument)

			// === SECOND FILE: Genparticipant with MsgSubmitNewParticipant + Authz grants ===
			genparticipantMessages := make([]sdk.Msg, 0)

			// Add MsgSubmitNewParticipant
			submitParticipantMsg := &inference.MsgSubmitNewParticipant{
				Creator:      addr.String(),
				Url:          urlStr,
				ValidatorKey: utils.PubKeyToString(valPubKey),
				WorkerKey:    "",
			}
			genparticipantMessages = append(genparticipantMessages, submitParticipantMsg)

			// Add authz grant messages for ML operational permissions
			// Grant permissions from cold key (operator) to warm key (ML operational address)
			expirationTime := time.Now().Add(365 * 24 * time.Hour) // 1 year expiration

			for _, msgType := range inferencepkg.InferenceOperationKeyPerms {
				authorization := authztypes.NewGenericAuthorization(sdk.MsgTypeURL(msgType))
				grantMsg, err := authztypes.NewMsgGrant(
					addr,                 // granter (cold key)
					mlOperationalAddress, // grantee (warm key)
					authorization,
					&expirationTime,
				)
				if err != nil {
					return errors.Wrapf(err, "failed to create MsgGrant for %s", sdk.MsgTypeURL(msgType))
				}
				genparticipantMessages = append(genparticipantMessages, grantMsg)
			}

			// Create and sign the genparticipant tx
			genparticipantTx, err := createAndSignTx(clientCtx, txFactory, name, genparticipantMessages)
			if err != nil {
				return errors.Wrap(err, "failed to create genparticipant tx")
			}

			// Write genparticipant file
			genparticipantOutputDocument, err := makeGenparticipantOutputFilepath(config.RootDir, nodeID)
			if err != nil {
				return errors.Wrap(err, "failed to create genparticipant output file path")
			}

			if err := writeSignedGenTx(clientCtx, genparticipantOutputDocument, genparticipantTx); err != nil {
				return errors.Wrap(err, "failed to write genparticipant tx")
			}

			cmd.PrintErrf("Genparticipant transaction written to %q\n", genparticipantOutputDocument)
			return nil
		},
	}

	cmd.Flags().String(flags.FlagHome, defaultNodeHome, "The application home directory")
	cmd.Flags().String(flags.FlagOutputDocument, "", "Write the genesis transaction JSON document to the given file instead of the default location")
	cmd.Flags().String("ml-operational-address", "", "Bech32 address of the ML operational key (warm key) - REQUIRED")
	cmd.Flags().String("url", "", "PUBLIC_URL of the node - REQUIRED")
	cmd.Flags().AddFlagSet(fsCreateValidator)
	flags.AddTxFlagsToCmd(cmd)
	_ = cmd.Flags().MarkHidden(flags.FlagOutput) // signing makes sense to output only json

	// Mark ml-operational-address as required
	_ = cmd.MarkFlagRequired("ml-operational-address")
	_ = cmd.MarkFlagRequired("url")

	return cmd
}

func makeClassicOutputFilepath(rootDir, nodeID string) (string, error) {
	writePath := filepath.Join(rootDir, "config", "gentx")
	if err := os.MkdirAll(writePath, 0o700); err != nil {
		return "", fmt.Errorf("could not create directory %q: %w", writePath, err)
	}

	return filepath.Join(writePath, fmt.Sprintf("gentx-%v.json", nodeID)), nil
}

func makeGenparticipantOutputFilepath(rootDir, nodeID string) (string, error) {
	writePath := filepath.Join(rootDir, "config", "genparticipant")
	if err := os.MkdirAll(writePath, 0o700); err != nil {
		return "", fmt.Errorf("could not create directory %q: %w", writePath, err)
	}

	return filepath.Join(writePath, fmt.Sprintf("genparticipant-%v.json", nodeID)), nil
}

func createAndSignTx(clientCtx client.Context, txFactory tx.Factory, keyName string, messages []sdk.Msg) (sdk.Tx, error) {
	// write the unsigned transaction to the buffer
	w := bytes.NewBuffer([]byte{})
	clientCtx = clientCtx.WithOutput(w)

	// Validate all messages
	for _, msg := range messages {
		if m, ok := msg.(sdk.HasValidateBasic); ok {
			if err := m.ValidateBasic(); err != nil {
				return nil, err
			}
		}
	}

	if err := txFactory.PrintUnsignedTx(clientCtx, messages...); err != nil {
		return nil, errors.Wrap(err, "failed to print unsigned std tx")
	}

	// read the transaction
	stdTx, err := readUnsignedGenTxFile(clientCtx, w)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read unsigned gen tx file")
	}

	// sign the transaction
	txBuilder, err := clientCtx.TxConfig.WrapTxBuilder(stdTx)
	if err != nil {
		return nil, fmt.Errorf("error creating tx builder: %w", err)
	}

	err = authclient.SignTx(txFactory, clientCtx, keyName, txBuilder, true, true)
	if err != nil {
		return nil, errors.Wrap(err, "failed to sign std tx")
	}

	return stdTx, nil
}

func readUnsignedGenTxFile(clientCtx client.Context, r io.Reader) (sdk.Tx, error) {
	bz, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	aTx, err := clientCtx.TxConfig.TxJSONDecoder()(bz)
	if err != nil {
		return nil, err
	}

	return aTx, err
}

func writeSignedGenTx(clientCtx client.Context, outputDocument string, tx sdk.Tx) error {
	outputFile, err := os.OpenFile(outputDocument, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer outputFile.Close()

	json, err := clientCtx.TxConfig.TxJSONEncoder()(tx)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(outputFile, "%s\n", json)

	return err
}
