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

	// MaxBatchRecipients is the maximum number of recipients in a single batch transfer
	MaxBatchRecipients = 500

	// MaxBatchCoinEntries is the maximum total number of coin entries across all outputs
	MaxBatchCoinEntries = 2000
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

	vestingEpochs, err := normalizeVestingEpochs(req.VestingEpochs)
	if err != nil {
		return nil, err
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

func normalizeVestingEpochs(raw uint64) (uint64, error) {
	if raw > MaxVestingEpochs {
		return 0, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "vesting epochs %d exceeds maximum allowed: %d", raw, MaxVestingEpochs)
	}
	if raw == 0 {
		return DefaultVestingEpochs, nil
	}
	return raw, nil
}

func (k msgServer) applyVestingSchedule(ctx sdk.Context, recipient string, amount sdk.Coins, vestingEpochs uint64) error {
	schedule, found := k.GetVestingSchedule(ctx, recipient)
	if !found {
		schedule = types.VestingSchedule{
			ParticipantAddress: recipient,
			EpochAmounts:       []types.EpochCoins{},
		}
	}

	requiredLength := int(vestingEpochs)
	for len(schedule.EpochAmounts) < requiredLength {
		schedule.EpochAmounts = append(schedule.EpochAmounts, types.EpochCoins{
			Coins: sdk.NewCoins(),
		})
	}

	for _, coin := range amount {
		epochsInt := math.NewInt(int64(vestingEpochs))
		amountPerEpoch := coin.Amount.Quo(epochsInt)
		remainder := coin.Amount.Mod(epochsInt)

		for i := 0; i < int(vestingEpochs); i++ {
			epochCoin := sdk.NewCoin(coin.Denom, amountPerEpoch)
			if i == 0 && !remainder.IsZero() {
				epochCoin = epochCoin.Add(sdk.NewCoin(coin.Denom, remainder))
			}
			schedule.EpochAmounts[i].Coins = schedule.EpochAmounts[i].Coins.Add(epochCoin)
		}
	}

	return k.SetVestingSchedule(ctx, schedule)
}
