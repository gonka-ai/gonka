package cmd

import (
	"fmt"
	"strconv"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/productscience/inference/x/inference/types"
	"github.com/spf13/cobra"
)

const (
	FlagTitle     = "title"
	FlagSummary   = "summary"
	FlagDeposit   = "deposit"
	FlagExpedited = "expedited"
)

// GetCmdSubmitPartialUpgrade implements a command handler for submitting a partial upgrade governance proposal
func GetCmdSubmitPartialUpgrade() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "partial-upgrade [height] [node-version] [api-binaries-json]",
		Args:  cobra.ExactArgs(3),
		Short: "Submit a partial upgrade governance proposal",
		Long: `Submit a governance proposal for a partial upgrade that will execute at the specified height.
The proposal will automatically set the governance module as the authority.

Example:
$ inferenced tx inference partial-upgrade 6965 "v3.0.8" "" \
  --title "Partial Upgrade to v3.0.8" \
  --summary "Upgrade node version to v3.0.8 at height 6965" \
  --deposit 50000000nicoin \
  --from genesis
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			// Parse height
			height, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid height: %w", err)
			}

			nodeVersion := args[1]
			apiBinariesJson := args[2]

			// Get governance module address as authority
			govModuleAddress := authtypes.NewModuleAddress(govtypes.ModuleName).String()

			// Create the partial upgrade message
			partialUpgradeMsg := &types.MsgCreatePartialUpgrade{
				Authority:       govModuleAddress,
				Height:          height,
				NodeVersion:     nodeVersion,
				ApiBinariesJson: apiBinariesJson,
			}

			// Get proposal flags
			title, _ := cmd.Flags().GetString(FlagTitle)
			summary, _ := cmd.Flags().GetString(FlagSummary)
			depositStr, _ := cmd.Flags().GetString(FlagDeposit)
			expedited, _ := cmd.Flags().GetBool(FlagExpedited)

			// Parse deposit
			deposit, err := sdk.ParseCoinsNormalized(depositStr)
			if err != nil {
				return fmt.Errorf("invalid deposit: %w", err)
			}

			// Create governance proposal
			proposalMsg, err := govv1.NewMsgSubmitProposal(
				[]sdk.Msg{partialUpgradeMsg},
				deposit,
				clientCtx.GetFromAddress().String(),
				"", // metadata
				title,
				summary,
				expedited,
			)
			if err != nil {
				return fmt.Errorf("failed to create proposal: %w", err)
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), proposalMsg)
		},
	}

	cmd.Flags().String(FlagTitle, "", "Title of the proposal")
	cmd.Flags().String(FlagSummary, "", "Summary of the proposal")
	cmd.Flags().String(FlagDeposit, "", "Deposit for the proposal")
	cmd.Flags().Bool(FlagExpedited, false, "Whether the proposal is expedited")

	flags.AddTxFlagsToCmd(cmd)

	return cmd
}
