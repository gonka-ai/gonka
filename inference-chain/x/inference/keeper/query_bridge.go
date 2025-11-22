package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// BridgeAddressesByChain queries bridge addresses by chain ID
func (k Keeper) BridgeAddressesByChain(ctx context.Context, req *types.QueryBridgeAddressesByChainRequest) (*types.QueryBridgeAddressesByChainResponse, error) {
	addresses := k.GetBridgeContractAddressesByChain(ctx, req.ChainId)
	return &types.QueryBridgeAddressesByChainResponse{
		Addresses: addresses,
	}, nil
}

// ApprovedTokensForTrade queries all approved bridge tokens for trading
func (k Keeper) ApprovedTokensForTrade(ctx context.Context, req *types.QueryApprovedTokensForTradeRequest) (*types.QueryApprovedTokensForTradeResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	approvedTokens := k.GetAllBridgeTradeApprovedTokens(sdkCtx)
	return &types.QueryApprovedTokensForTradeResponse{
		ApprovedTokens: approvedTokens,
	}, nil
}
