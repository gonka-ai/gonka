package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) LiquidityPool(goCtx context.Context, req *types.QueryLiquidityPoolRequest) (*types.QueryLiquidityPoolResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Get the singleton liquidity pool
	pool, found := k.GetLiquidityPool(ctx)
	if !found {
		return nil, status.Error(codes.NotFound, "liquidity pool not found")
	}

	return &types.QueryLiquidityPoolResponse{
		Address:     pool.Address,
		CodeId:      pool.CodeId,
		BlockHeight: pool.BlockHeight,
	}, nil
}
