package inference

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference"
	"github.com/productscience/inference/x/inference/types"
	"github.com/spf13/cobra"
)

func GrantMLOpsPermissionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant-ml-ops-permissions <account-key-name> <ml-operational-address>",
		Short: "Grant ML operations permissions from account key to ML operational key",
		Long: `Grant all ML operations permissions from account key to ML operational key.

This allows the ML operational key to perform automated ML operations on behalf of the account key.
The account key retains full control and can revoke these permissions at any time.

Arguments:
  account-key-name         Name of the account key in keyring (cold wallet)
  ml-operational-address   Bech32 address of the ML operational key (hot wallet)

Example:
  inferenced tx inference grant-ml-ops-permissions \
    gonka-account-key \
    gonka1rk52j24xj9ej87jas4zqpvjuhrgpnd7h3feqmm \
    --from gonka-account-key \
    --node http://node2.gonka.ai:8000/chain-rpc/

Note: Chain ID will be auto-detected from the chain if not specified with --chain-id`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			status, err := clientCtx.Client.Status(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to query chain status for chain-id: %w", err)
			}

			chainID := status.NodeInfo.Network
			cmd.Printf("Detected chain-id: %s\n", chainID)

			clientCtx = clientCtx.WithChainID(chainID)

			accountKeyName := args[0]
			mlOperationalAddressStr := args[1]

			mlOperationalAddress, err := sdk.AccAddressFromBech32(mlOperationalAddressStr)
			if err != nil {
				return fmt.Errorf("invalid ML operational address: %w", err)
			}

			txFactory, err := tx.NewFactoryCLI(clientCtx, cmd.Flags())
			if err != nil {
				return err
			}

			txFactory = txFactory.WithChainID(clientCtx.ChainID)

			return inference.GrantMLOperationalKeyPermissionsToAccount(
				cmd.Context(),
				clientCtx,
				txFactory,
				accountKeyName,
				mlOperationalAddress,
				nil, // Use default expiration (1 year)
			)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

type settlementFileJSON struct {
	EscrowID   string                    `json:"escrow_id"`
	StateRoot  string                    `json:"state_root"`
	Nonce      uint64                    `json:"nonce"`
	RestHash   string                    `json:"rest_hash"`
	HostStats  []settlementHostStatsJSON `json:"host_stats"`
	Signatures []slotSignatureJSON       `json:"signatures"`
}

type settlementHostStatsJSON struct {
	SlotID               uint32 `json:"slot_id"`
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

type slotSignatureJSON struct {
	SlotID    uint32 `json:"slot_id"`
	Signature string `json:"signature"`
}

func SettleSubnetEscrowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settle-subnet-escrow <settlement-file.json>",
		Short: "Settle a subnet escrow using a settlement JSON file produced by subnetctl",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read settlement file: %w", err)
			}

			var sf settlementFileJSON
			if err := json.Unmarshal(data, &sf); err != nil {
				return fmt.Errorf("parse settlement JSON: %w", err)
			}

			escrowID, err := strconv.ParseUint(sf.EscrowID, 10, 64)
			if err != nil {
				return fmt.Errorf("parse escrow_id: %w", err)
			}

			stateRoot, err := base64.StdEncoding.DecodeString(sf.StateRoot)
			if err != nil {
				return fmt.Errorf("decode state_root: %w", err)
			}

			restHash, err := base64.StdEncoding.DecodeString(sf.RestHash)
			if err != nil {
				return fmt.Errorf("decode rest_hash: %w", err)
			}

			hostStats := make([]*types.SubnetSettlementHostStats, len(sf.HostStats))
			for i, hs := range sf.HostStats {
				hostStats[i] = &types.SubnetSettlementHostStats{
					SlotId:               hs.SlotID,
					Missed:               hs.Missed,
					Invalid:              hs.Invalid,
					Cost:                 hs.Cost,
					RequiredValidations:  hs.RequiredValidations,
					CompletedValidations: hs.CompletedValidations,
				}
			}

			sigs := make([]*types.SubnetSlotSignature, len(sf.Signatures))
			for i, s := range sf.Signatures {
				sigBytes, err := base64.StdEncoding.DecodeString(s.Signature)
				if err != nil {
					return fmt.Errorf("decode signature for slot %d: %w", s.SlotID, err)
				}
				sigs[i] = &types.SubnetSlotSignature{
					SlotId:    s.SlotID,
					Signature: sigBytes,
				}
			}

			msg := &types.MsgSettleSubnetEscrow{
				Settler:    clientCtx.GetFromAddress().String(),
				EscrowId:   escrowID,
				StateRoot:  stateRoot,
				Nonce:      sf.Nonce,
				RestHash:   restHash,
				HostStats:  hostStats,
				Signatures: sigs,
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
