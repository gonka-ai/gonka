package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ValidateWrappedTokenForTrade handles the query to validate a wrapped token for trading
func (k Keeper) ValidateWrappedTokenForTrade(ctx context.Context, req *types.QueryValidateWrappedTokenForTradeRequest) (*types.QueryValidateWrappedTokenForTradeResponse, error) {
	if req == nil {
		k.LogError("Bridge exchange: ValidateWrappedTokenForTrade received nil request", types.Messages)
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.ContractAddress == "" {
		k.LogError("Bridge exchange: ValidateWrappedTokenForTrade missing contract address", types.Messages)
		return nil, status.Error(codes.InvalidArgument, "contract address cannot be empty")
	}

	k.LogInfo("Bridge exchange: ValidateWrappedTokenForTrade called", types.Messages, "contract", req.ContractAddress)

	// Use the existing validation function
	isValid, _, err := k.validateWrappedTokenForTradeInternal(ctx, req.ContractAddress)
	if err != nil {
		// Log the validation error for observability; return false for contract compatibility
		k.LogError("Bridge exchange: ValidateWrappedTokenForTrade validation error", types.Messages, "contract", req.ContractAddress, "error", err)
		return &types.QueryValidateWrappedTokenForTradeResponse{
			IsValid: false,
		}, nil
	}

	k.LogInfo("Bridge exchange: ValidateWrappedTokenForTrade completed", types.Messages, "contract", req.ContractAddress, "is_valid", isValid)

	return &types.QueryValidateWrappedTokenForTradeResponse{
		IsValid: isValid,
	}, nil
}
