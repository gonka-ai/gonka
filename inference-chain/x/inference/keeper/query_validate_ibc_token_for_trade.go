package keeper

import (
	"context"
	"strings"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ValidateIbcTokenForTrade handles the query to validate an IBC token for trading
func (k Keeper) ValidateIbcTokenForTrade(ctx context.Context, req *types.QueryValidateIbcTokenForTradeRequest) (*types.QueryValidateIbcTokenForTradeResponse, error) {
	if req == nil {
		k.LogError("IBC trade validation: ValidateIbcTokenForTrade received nil request", types.Messages)
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ibcDenom := strings.TrimSpace(req.IbcDenom)

	if ibcDenom == "" {
		k.LogError("IBC trade validation: ValidateIbcTokenForTrade missing ibc denom", types.Messages)
		return nil, status.Error(codes.InvalidArgument, "ibc_denom cannot be empty")
	}

	k.LogInfo("IBC trade validation: ValidateIbcTokenForTrade called", types.Messages, "denom", ibcDenom)

	// Use the existing validation function
	isValid, metadata, err := k.validateIBCTokenForTradeInternal(ctx, ibcDenom)
	if err != nil {
		// Log the validation error for observability; return false for contract compatibility
		k.LogError("IBC trade validation: ValidateIbcTokenForTrade validation error", types.Messages, "denom", ibcDenom, "error", err)
		return &types.QueryValidateIbcTokenForTradeResponse{
			IsValid: false,
		}, nil
	}

	// Extract decimals from metadata
	// 1. Find the display denom
	// 2. Find the unit in DenomUnits that matches display denom
	// 3. Get its exponent
	var decimals uint32 = 0
	displayDenom := metadata.Display
	for _, unit := range metadata.DenomUnits {
		if unit.Denom == displayDenom {
			decimals = unit.Exponent
			break
		}
	}

	k.LogInfo("IBC trade validation: ValidateIbcTokenForTrade completed", types.Messages, "denom", ibcDenom, "is_valid", isValid, "decimals", decimals)

	return &types.QueryValidateIbcTokenForTradeResponse{
		IsValid:  isValid,
		Decimals: decimals,
	}, nil
}
