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

	if len(req.Outputs) == 0 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "outputs cannot be empty")
	}
	if len(req.Outputs) > MaxBatchRecipients {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many recipients: %d, max allowed: %d", len(req.Outputs), MaxBatchRecipients)
	}

	vestingEpochs, err := normalizeVestingEpochs(req.VestingEpochs)
	if err != nil {
		return nil, err
	}

	// Aggregate duplicate recipients for deterministic and efficient schedule updates.
	aggregated := make(map[string]sdk.Coins, len(req.Outputs))
	totalCoinEntries := 0
	for _, output := range req.Outputs {
		if _, err := sdk.AccAddressFromBech32(output.Recipient); err != nil {
			return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid recipient address: %s", err)
		}
		if output.Amount.IsZero() {
			return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidCoins, "amount cannot be zero for recipient %s", output.Recipient)
		}
		if !output.Amount.IsValid() {
			return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidCoins, "invalid coins for recipient %s", output.Recipient)
		}
		if len(output.Amount) > MaxCoinsInAmount {
			return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many coin denominations for recipient %s: %d, max allowed: %d", output.Recipient, len(output.Amount), MaxCoinsInAmount)
		}

		totalCoinEntries += len(output.Amount)
		if totalCoinEntries > MaxBatchCoinEntries {
			return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many total coin entries in batch: %d, max allowed: %d", totalCoinEntries, MaxBatchCoinEntries)
		}

		aggregated[output.Recipient] = aggregated[output.Recipient].Add(output.Amount...)
	}

	totalAmount := sdk.NewCoins()
	recipients := make([]string, 0, len(aggregated))
	for recipient, amount := range aggregated {
		recipients = append(recipients, recipient)
		totalAmount = totalAmount.Add(amount...)
	}
	slices.Sort(recipients)

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
