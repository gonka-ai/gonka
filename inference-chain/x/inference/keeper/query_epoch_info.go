package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochInfo(goCtx context.Context, req *types.QueryEpochInfoRequest) (*types.QueryEpochInfoResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	params := k.GetParams(ctx)
	latestEpoch, found := k.GetLatestEpoch(ctx)
	if !found {
		k.LogError("GetLatestEpoch returned false, no latest epoch found", types.EpochGroup)
		return nil, types.ErrLatestEpochNotFound
	}
	if latestEpoch == nil {
		k.LogError("GetLatestEpoch returned nil, no latest epoch found", types.EpochGroup)
		return nil, types.ErrLatestEpochNotFound
	}

	return &types.QueryEpochInfoResponse{
		BlockHeight: ctx.BlockHeight(),
		Params:      params,
		LatestEpoch: *latestEpoch,
	}, nil
}
