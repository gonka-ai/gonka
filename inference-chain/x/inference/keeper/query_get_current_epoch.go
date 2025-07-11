package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetCurrentEpoch(goCtx context.Context, req *types.QueryGetCurrentEpochRequest) (*types.QueryGetCurrentEpochResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("GetCurrentEpoch: No effective epoch found", types.EpochGroup)
		return nil, status.Error(codes.NotFound, "no effective epoch found")
	}

	return &types.QueryGetCurrentEpochResponse{
		Epoch: epochIndex,
	}, nil
}
