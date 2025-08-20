package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	"github.com/productscience/inference/x/restrictions/types"
)

// ExecuteEmergencyTransfer executes a governance-approved emergency transfer
func (k msgServer) ExecuteEmergencyTransfer(goCtx context.Context, req *types.MsgExecuteEmergencyTransfer) (*types.MsgExecuteEmergencyTransferResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Get current parameters
	params := k.GetParams(ctx)

	// Find the exemption template
	var exemption *types.EmergencyTransferExemption
	for _, e := range params.EmergencyTransferExemptions {
		if e.ExemptionId == req.ExemptionId {
			exemption = &e
			break
		}
	}

	if exemption == nil {
		return nil, errorsmod.Wrapf(types.ErrExemptionNotFound, "exemption ID %s not found", req.ExemptionId)
	}

	// Check if exemption has expired
	if ctx.BlockHeight() > int64(exemption.ExpiryBlock) {
		return nil, errorsmod.Wrapf(types.ErrExemptionExpired, "exemption %s expired at block %d", req.ExemptionId, exemption.ExpiryBlock)
	}

	// Validate addresses match exemption
	if exemption.FromAddress != "*" && exemption.FromAddress != req.FromAddress {
		return nil, errorsmod.Wrapf(types.ErrInvalidExemptionMatch, "from address %s does not match exemption %s", req.FromAddress, exemption.FromAddress)
	}

	if exemption.ToAddress != "*" && exemption.ToAddress != req.ToAddress {
		return nil, errorsmod.Wrapf(types.ErrInvalidExemptionMatch, "to address %s does not match exemption %s", req.ToAddress, exemption.ToAddress)
	}

	// Parse and validate amount
	amount, err := strconv.ParseUint(req.Amount, 10, 64)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid amount format: %s", err)
	}

	maxAmount, err := strconv.ParseUint(exemption.MaxAmount, 10, 64)
	if err != nil {
		return nil, errorsmod.Wrapf(types.ErrInvalidExemption, "invalid max amount in exemption: %s", err)
	}

	if amount > maxAmount {
		return nil, errorsmod.Wrapf(types.ErrExemptionAmountExceeded, "amount %d exceeds maximum allowed %d", amount, maxAmount)
	}

	// Check usage limits
	currentUsage := uint64(0)
	usageIndex := -1
	for i, usage := range params.ExemptionUsageTracking {
		if usage.ExemptionId == req.ExemptionId && usage.AccountAddress == req.FromAddress {
			currentUsage = usage.UsageCount
			usageIndex = i
			break
		}
	}

	if currentUsage >= exemption.UsageLimit {
		return nil, errorsmod.Wrapf(types.ErrExemptionUsageLimitExceeded, "usage limit %d exceeded for exemption %s", exemption.UsageLimit, req.ExemptionId)
	}

	// Execute the transfer
	fromAddr, err := sdk.AccAddressFromBech32(req.FromAddress)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid from address: %s", err)
	}

	toAddr, err := sdk.AccAddressFromBech32(req.ToAddress)
	if err != nil {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid to address: %s", err)
	}

	coins := sdk.NewCoins(sdk.NewCoin(req.Denom, math.NewInt(int64(amount))))

	// Use bank keeper to send coins (this bypasses SendRestriction as it's a direct keeper call)
	if err := k.bankKeeper.SendCoins(ctx, fromAddr, toAddr, coins); err != nil {
		return nil, errorsmod.Wrap(err, "failed to execute emergency transfer")
	}

	// Update usage tracking
	if usageIndex >= 0 {
		// Update existing usage
		params.ExemptionUsageTracking[usageIndex].UsageCount++
	} else {
		// Add new usage entry
		params.ExemptionUsageTracking = append(params.ExemptionUsageTracking, types.ExemptionUsage{
			ExemptionId:    req.ExemptionId,
			AccountAddress: req.FromAddress,
			UsageCount:     1,
		})
	}

	// Save updated parameters
	if err := k.SetParams(ctx, params); err != nil {
		return nil, errorsmod.Wrap(err, "failed to update usage tracking")
	}

	// Calculate remaining uses
	remainingUses := exemption.UsageLimit - (currentUsage + 1)

	// Emit event
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeEmergencyTransfer,
			sdk.NewAttribute(types.AttributeKeyExemptionId, req.ExemptionId),
			sdk.NewAttribute(types.AttributeKeyFromAddress, req.FromAddress),
			sdk.NewAttribute(types.AttributeKeyToAddress, req.ToAddress),
			sdk.NewAttribute(types.AttributeKeyAmount, req.Amount),
			sdk.NewAttribute(types.AttributeKeyDenom, req.Denom),
			sdk.NewAttribute(types.AttributeKeyRemainingUses, strconv.FormatUint(remainingUses, 10)),
		),
	)

	return &types.MsgExecuteEmergencyTransferResponse{
		RemainingUses: remainingUses,
	}, nil
}
