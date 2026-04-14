package cmd

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/spf13/cobra"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func PublishPubKeyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish-pubkey",
		Short: "Publish local account pubkey on-chain",
		Long: `Sends a minimal self-transfer (1ngonka) from the selected account to itself, which stores the account pubkey on-chain.

Use --gas-prices 10ngonka (or higher) to set transaction fees.

Example:
  inferenced publish-pubkey --from gonka-account-key --gas-prices 10ngonka --node http://node2.gonka.ai:8000/chain-rpc/`,
		RunE:  publishPubKey,
	}

	flags.AddTxFlagsToCmd(cmd)
	_ = cmd.MarkFlagRequired(flags.FlagFrom)
	_ = cmd.MarkFlagRequired(flags.FlagNode)

	return cmd
}

func publishPubKey(cmd *cobra.Command, _ []string) error {
	clientCtx, err := client.GetClientTxContext(cmd)
	if err != nil {
		return err
	}

	from := clientCtx.GetFromAddress().String()
	msg := &banktypes.MsgSend{
		FromAddress: from,
		ToAddress:   from,
		Amount:      sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 1)),
	}

	cmd.Printf("Publishing pubkey for %s via self-transfer 1%s\n", from, inferencetypes.BaseCoin)
	return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
}
