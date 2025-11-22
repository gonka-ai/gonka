package keeper

import (
	"context"
	"strings"

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

	contractAddress := strings.TrimSpace(req.ContractAddress)

	if contractAddress == "" {
		k.LogError("Bridge exchange: ValidateWrappedTokenForTrade missing contract address", types.Messages)
		return nil, status.Error(codes.InvalidArgument, "contract address cannot be empty")
	}

	k.LogInfo("Bridge exchange: ValidateWrappedTokenForTrade called", types.Messages, "contract", contractAddress)

	// Use the existing validation function
	isValid, _, err := k.validateWrappedTokenForTradeInternal(ctx, contractAddress)
	if err != nil {
		// Log the validation error for observability; return false for contract compatibility
		k.LogError("Bridge exchange: ValidateWrappedTokenForTrade validation error", types.Messages, "contract", contractAddress, "error", err)
		return &types.QueryValidateWrappedTokenForTradeResponse{
			IsValid: false,
		}, nil
	}

	k.LogInfo("Bridge exchange: ValidateWrappedTokenForTrade completed", types.Messages, "contract", contractAddress, "is_valid", isValid)

	return &types.QueryValidateWrappedTokenForTradeResponse{
		IsValid: isValid,
	}, nil
}
