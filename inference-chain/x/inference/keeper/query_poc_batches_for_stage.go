package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PocBatchesForStage(goCtx context.Context, req *types.QueryPocBatchesForStageRequest) (*types.QueryPocBatchesForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	pocBatches, err := k.GetBatchesByPoCStage(ctx, req.BlockHeight)
	if err != nil {
		k.LogError("failed to get PoC batches", "err", err)
		return nil, status.Error(codes.Internal, "failed to get PoC batches")
	}

	return &types.QueryPocBatchesForStageResponse{}, nil
}
