package keeper

import (
	"context"
	"fmt"
	"slices"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	"github.com/productscience/inference/x/streamvesting/types"
)

func (k msgServer) BatchTransferWithVesting(goCtx context.Context, req *types.MsgBatchTransferWithVesting) (*types.MsgBatchTransferWithVestingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	senderAddr, err := sdk.AccAddressFromBech32(req.Sender)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address: %s", err)
	}

	if !k.isAllowedVestingSender(req.Sender) {
		return nil, errorsmod.Wrapf(types.ErrUnauthorizedSender, "sender %s is not authorized to execute vesting transfers", req.Sender)
	}

	vestingEpochs := normalizeVestingEpochs(req.VestingEpochs)

	// Aggregate duplicate recipients for deterministic and efficient schedule updates.
	aggregated := make(map[string]sdk.Coins, len(req.Outputs))
	for _, output := range req.Outputs {
		aggregated[output.Recipient] = aggregated[output.Recipient].Add(output.Amount...)

	}

	recipients := make([]string, 0, len(aggregated))
	for recipient := range aggregated {
		recipients = append(recipients, recipient)
	}
	slices.Sort(recipients)

	totalAmount := sdk.NewCoins()
	for _, recipient := range recipients {
		totalAmount = totalAmount.Add(aggregated[recipient]...)
	}

	if err := k.bookkeepingBankKeeper.SendCoinsFromAccountToModule(ctx, senderAddr, types.ModuleName, totalAmount, "batch transfer with vesting"); err != nil {
		return nil, errorsmod.Wrapf(err, "failed to transfer coins from sender to module")
	}

	for _, recipient := range recipients {
		amount := aggregated[recipient]
		for _, coin := range amount {
			k.bookkeepingBankKeeper.LogSubAccountTransaction(
				ctx,
				types.ModuleName,
				recipient,
				HoldingSubAccount,
				coin,
				fmt.Sprintf("batch transfer with vesting from %s", req.Sender),
			)
		}

		if err := k.applyVestingSchedule(ctx, recipient, amount, vestingEpochs); err != nil {
			return nil, errorsmod.Wrapf(err, "failed to set vesting schedule for recipient %s", recipient)
		}
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeBatchTransferWithVesting,
			sdk.NewAttribute(types.AttributeKeySender, req.Sender),
			sdk.NewAttribute(types.AttributeKeyAmount, totalAmount.String()),
			sdk.NewAttribute(types.AttributeKeyVestingEpochs, fmt.Sprintf("%d", vestingEpochs)),
			sdk.NewAttribute(types.AttributeKeyRecipientsCount, fmt.Sprintf("%d", len(recipients))),
		),
	)

	k.Logger().Info("Batch transfer with vesting completed",
		"sender", req.Sender,
		"recipients_count", len(recipients),
		"amount", totalAmount,
		"vesting_epochs", vestingEpochs,
	)

	return &types.MsgBatchTransferWithVestingResponse{}, nil
}
