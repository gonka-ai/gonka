package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/collateral/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func (k msgServer) DepositCollateral(goCtx context.Context, msg *types.MsgDepositCollateral) (*types.MsgDepositCollateralResponse, error) {
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

	// Transfer tokens from the participant to the module account
	err = k.bookkeepingBankKeeper.SendCoinsFromAccountToModule(ctx, participantAddr, types.ModuleName, sdk.NewCoins(msg.Amount), "collateral deposit")
	if err != nil {
		return nil, err
	}

	// Get the current collateral (if any)
	currentCollateral, found := k.GetCollateral(ctx, participantAddr)
	if found {
		// Add to existing collateral (denom check not needed since we enforce single denom)
		currentCollateral = currentCollateral.Add(msg.Amount)
	} else {
		// First deposit
		currentCollateral = msg.Amount
	}

	k.bookkeepingBankKeeper.LogSubAccountTransaction(goCtx, types.ModuleName, msg.Participant, types.SubAccountCollateral, msg.Amount, "collateral deposit")

	// Store the updated collateral
	k.SetCollateral(ctx, participantAddr, currentCollateral)

	// Emit deposit event
	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventTypeDepositCollateral,
			sdk.NewAttribute(types.AttributeKeyParticipant, msg.Participant),
			sdk.NewAttribute(types.AttributeKeyAmount, msg.Amount.String()),
		),
	})

	k.Logger().Info("collateral deposited",
		"participant", msg.Participant,
		"amount", msg.Amount.String(),
		"total_collateral", currentCollateral.String(),
	)

	return &types.MsgDepositCollateralResponse{}, nil
}
