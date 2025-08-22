package keeper

import (
	"context"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/collateral/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func (k msgServer) WithdrawCollateral(goCtx context.Context, msg *types.MsgWithdrawCollateral) (*types.MsgWithdrawCollateralResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate the participant address
	participantAddr, err := sdk.AccAddressFromBech32(msg.Participant)
	if err != nil {
		return nil, err
	}

	// Ensure only base denomination is accepted
	if msg.Amount.Denom != inferencetypes.BaseCoin {
		return nil, types.ErrInvalidDenom.Wrapf("only %s denomination is accepted for collateral, got %s",
			inferencetypes.BaseCoin, msg.Amount.Denom)
	}

	// Get the participant's current collateral
	currentCollateral, found := k.GetCollateral(ctx, participantAddr)
	if !found {
		return nil, types.ErrNoCollateralFound.Wrapf("participant %s has no collateral", msg.Participant)
	}

	// Ensure they have enough collateral to withdraw
	if currentCollateral.IsLT(msg.Amount) {
		return nil, types.ErrInsufficientCollateral.Wrapf("collateral %s is less than withdrawal amount %s",
			currentCollateral.String(), msg.Amount.String())
	}

	// Get the current epoch from the collateral module's own state
	currentEpoch := k.GetCurrentEpoch(ctx)

	// Get the unbonding period from params
	params := k.GetParams(ctx)

	// Calculate the completion epoch
	completionEpoch := currentEpoch + params.UnbondingPeriodEpochs

	k.Logger().Info("adding unbonding entry for collateral withdrawal",
		"participant", msg.Participant,
		"amount", msg.Amount.String(),
		"completion_epoch", completionEpoch,
		"current_epoch", currentEpoch,
		"unbonding_period_epochs", params.UnbondingPeriodEpochs,
	)
	// Create the unbonding entry
	k.AddUnbondingCollateral(ctx, participantAddr, completionEpoch, msg.Amount)

	// Reduce the active collateral
	newCollateral := currentCollateral.Sub(msg.Amount)
	if newCollateral.IsZero() {
		k.RemoveCollateral(ctx, participantAddr)
	} else {
		k.SetCollateral(ctx, participantAddr, newCollateral)
	}

	k.bookkeepingBankKeeper.LogSubAccountTransaction(goCtx, msg.Participant, types.ModuleName, types.SubAccountCollateral, msg.Amount, "collateral to unbonding")
	k.bookkeepingBankKeeper.LogSubAccountTransaction(goCtx, types.ModuleName, msg.Participant, types.SubAccountUnbonding, msg.Amount, "collateral to unbonding")

	// Emit withdrawal event
	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeWithdrawCollateral,
			sdk.NewAttribute(types.AttributeKeyParticipant, msg.Participant),
			sdk.NewAttribute(types.AttributeKeyAmount, msg.Amount.String()),
			sdk.NewAttribute(types.AttributeKeyCompletionEpoch, strconv.FormatUint(completionEpoch, 10)),
		),
	})

	k.Logger().Info("collateral withdrawal initiated",
		"participant", msg.Participant,
		"amount", msg.Amount.String(),
		"completion_epoch", completionEpoch,
	)

	return &types.MsgWithdrawCollateralResponse{
		CompletionEpoch: completionEpoch,
	}, nil
}
