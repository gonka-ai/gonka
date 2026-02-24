package keeper

import (
	"context"
	"fmt"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	"github.com/productscience/inference/x/streamvesting/types"
)

const (
	// DefaultVestingEpochs is the default number of epochs for vesting (180 epochs)
	DefaultVestingEpochs = uint64(180)

	// MaxVestingEpochs is the maximum allowed vesting epochs to prevent DoS
	MaxVestingEpochs = uint64(3650) // ~10 years

	// MaxCoinsInAmount is the maximum number of coin denominations in a single transfer
	MaxCoinsInAmount = 10
)

func (k msgServer) TransferWithVesting(goCtx context.Context, req *types.MsgTransferWithVesting) (*types.MsgTransferWithVestingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate sender address
	senderAddr, err := sdk.AccAddressFromBech32(req.Sender)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address: %s", err)
	}

	// Validate recipient address
	_, err = sdk.AccAddressFromBech32(req.Recipient)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid recipient address: %s", err)
	}

	// Validate amount
	if req.Amount.IsZero() {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidCoins, "amount cannot be zero")
	}

	if !req.Amount.IsValid() {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidCoins, "invalid coins")
	}

	// Validate number of coin denominations to prevent N*M complexity DoS
	if len(req.Amount) > MaxCoinsInAmount {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many coin denominations: %d, max allowed: %d", len(req.Amount), MaxCoinsInAmount)
	}

	// Validate vesting epochs upper limit to prevent DoS
	if req.VestingEpochs > MaxVestingEpochs {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "vesting epochs %d exceeds maximum allowed: %d", req.VestingEpochs, MaxVestingEpochs)
	}

	// Determine vesting epochs - use default if not specified or zero
	vestingEpochs := req.VestingEpochs
	if vestingEpochs == 0 {
		vestingEpochs = DefaultVestingEpochs
	}

	// Transfer coins from sender to the streamvesting module
	err = k.bookkeepingBankKeeper.SendCoinsFromAccountToModule(ctx, senderAddr, types.ModuleName, req.Amount, "transfer with vesting")
	if err != nil {
		return nil, errorsmod.Wrapf(err, "failed to transfer coins from sender to module")
	}

	// Log sub-account transaction for each coin
	for _, coin := range req.Amount {
		k.bookkeepingBankKeeper.LogSubAccountTransaction(ctx, types.ModuleName, req.Recipient, HoldingSubAccount,
			coin, fmt.Sprintf("transfer with vesting from %s", req.Sender))
	}

	// Get or create vesting schedule for recipient
	schedule, found := k.GetVestingSchedule(ctx, req.Recipient)
	if !found {
		schedule = types.VestingSchedule{
			ParticipantAddress: req.Recipient,
			EpochAmounts:       []types.EpochCoins{},
		}
	}

	// Extend the schedule if necessary
	requiredLength := int64(vestingEpochs)
	for int64(len(schedule.EpochAmounts)) < requiredLength {
		schedule.EpochAmounts = append(schedule.EpochAmounts, types.EpochCoins{
			Coins: sdk.NewCoins(),
		})
	}

	// Implement aggregation logic for each coin denomination
	for _, coin := range req.Amount {
		// Divide amount by epochs
		// if amount < vestingEpochs then the whole sum will be transferred during the first epoch
		epochsInt := math.NewInt(int64(vestingEpochs))
		amountPerEpoch := coin.Amount.Quo(epochsInt)
		remainder := coin.Amount.Mod(epochsInt)

		// Add the base amount to each epoch
		for i := int64(0); i < int64(vestingEpochs); i++ {
			epochCoin := sdk.NewCoin(coin.Denom, amountPerEpoch)

			// Add remainder to the first epoch
			if i == 0 && !remainder.IsZero() {
				epochCoin = epochCoin.Add(sdk.NewCoin(coin.Denom, remainder))
			}

			// Add to existing amount in this epoch
			schedule.EpochAmounts[i].Coins = schedule.EpochAmounts[i].Coins.Add(epochCoin)
		}
	}

	// Store the updated schedule
	err = k.SetVestingSchedule(ctx, schedule)
	if err != nil {
		k.Logger().Error("Failed to set vesting schedule for recipient", "recipient", req.Recipient, "error", err)
		return nil, errorsmod.Wrapf(err, "failed to set vesting schedule for recipient")
	}

	// Emit event
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeTransferWithVesting,
			sdk.NewAttribute(types.AttributeKeySender, req.Sender),
			sdk.NewAttribute(types.AttributeKeyRecipient, req.Recipient),
			sdk.NewAttribute(types.AttributeKeyAmount, req.Amount.String()),
			sdk.NewAttribute(types.AttributeKeyVestingEpochs, fmt.Sprintf("%d", vestingEpochs)),
		),
	)

	k.Logger().Info("Transfer with vesting completed",
		"sender", req.Sender,
		"recipient", req.Recipient,
		"amount", req.Amount,
		"vesting_epochs", vestingEpochs)

	return &types.MsgTransferWithVestingResponse{}, nil
}
