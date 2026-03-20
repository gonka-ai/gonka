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

func (k msgServer) TransferWithVesting(goCtx context.Context, req *types.MsgTransferWithVesting) (*types.MsgTransferWithVestingResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	senderAddr, err := sdk.AccAddressFromBech32(req.Sender)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address: %s", err)
	}

	if !k.isAllowedVestingSender(req.Sender) {
		return nil, errorsmod.Wrapf(types.ErrUnauthorizedSender, "sender %s is not authorized to execute vesting transfers", req.Sender)
	}

	vestingEpochs := normalizeVestingEpochs(req.VestingEpochs)

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

	if err := k.applyVestingSchedule(ctx, req.Recipient, req.Amount, vestingEpochs); err != nil {
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

func normalizeVestingEpochs(raw uint64) uint64 {
	if raw == 0 {
		return types.DefaultVestingEpochs
	}
	return raw
}

func (k msgServer) applyVestingSchedule(ctx sdk.Context, recipient string, amount sdk.Coins, vestingEpochs uint64) error {
	schedule, found := k.GetVestingSchedule(ctx, recipient)
	if !found {
		schedule = types.VestingSchedule{
			ParticipantAddress: recipient,
			EpochAmounts:       []types.EpochCoins{},
		}
	}

	// 1. Pre-allocate missing capacity to avoid continuous slice re-allocations
	requiredLength := vestingEpochs
	currentLen := uint64(len(schedule.EpochAmounts))
	if currentLen < requiredLength {
		missing := requiredLength - currentLen
		extension := make([]types.EpochCoins, missing)
		for i := uint64(0); i < missing; i++ {
			extension[i] = types.EpochCoins{Coins: sdk.NewCoins()}
		}
		schedule.EpochAmounts = append(schedule.EpochAmounts, extension...)
	}

	// 2. Pre-calculate the exact coin bundles to avoid inner-loop allocations
	firstEpochCoins := sdk.NewCoins()
	baseEpochCoins := sdk.NewCoins()
	epochsInt := math.NewInt(int64(vestingEpochs))

	for _, coin := range amount {
		amountPerEpoch := coin.Amount.Quo(epochsInt)
		remainder := coin.Amount.Mod(epochsInt)

		if !amountPerEpoch.IsZero() {
			baseEpochCoins = baseEpochCoins.Add(sdk.NewCoin(coin.Denom, amountPerEpoch))
		}

		firstAmount := amountPerEpoch.Add(remainder)
		if !firstAmount.IsZero() {
			firstEpochCoins = firstEpochCoins.Add(sdk.NewCoin(coin.Denom, firstAmount))
		}
	}

	// 3. Apply the pre-calculated bundles in a flat, single-pass loop
	for i := uint64(0); i < requiredLength; i++ {
		if i == 0 {
			if !firstEpochCoins.Empty() {
				schedule.EpochAmounts[0].Coins = schedule.EpochAmounts[0].Coins.Add(firstEpochCoins...)
			}
		} else {
			if !baseEpochCoins.Empty() {
				schedule.EpochAmounts[i].Coins = schedule.EpochAmounts[i].Coins.Add(baseEpochCoins...)
			}
		}
	}

	return k.SetVestingSchedule(ctx, schedule)
}
