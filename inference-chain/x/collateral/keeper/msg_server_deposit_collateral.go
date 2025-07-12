package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	"github.com/productscience/inference/x/collateral/types"
)

func (k msgServer) DepositCollateral(goCtx context.Context, msg *types.MsgDepositCollateral) (*types.MsgDepositCollateralResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate the participant address
	participantAddr, err := sdk.AccAddressFromBech32(msg.Participant)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid participant address: %s", err)
	}

	// Transfer coins from participant to module account
	err = k.bankEscrowKeeper.SendCoinsFromAccountToModule(ctx, participantAddr, types.ModuleName, sdk.NewCoins(msg.Amount))
	if err != nil {
		return nil, errorsmod.Wrapf(err, "failed to transfer collateral to module account")
	}

	// Get existing collateral (if any)
	existingCollateral, found := k.GetCollateral(ctx, msg.Participant)

	// Calculate new collateral amount
	var newAmount sdk.Coin
	if found && existingCollateral.Denom == msg.Amount.Denom {
		// Add to existing collateral
		newAmount = existingCollateral.Add(msg.Amount)
	} else if found {
		// Different denom - for now we'll reject this case
		// In the future, we might support multiple denoms
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"collateral denom mismatch: existing %s, depositing %s",
			existingCollateral.Denom, msg.Amount.Denom)
	} else {
		// First deposit
		newAmount = msg.Amount
	}

	// Store the updated collateral amount
	k.SetCollateral(ctx, msg.Participant, newAmount)

	// Emit event
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
		"total", newAmount.String(),
	)

	return &types.MsgDepositCollateralResponse{}, nil
}
