package keeper

import (
	"context"

    "github.com/productscience/inference/x/inference/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) CountPoCbatchesAtHeight(goCtx context.Context, req *types.QueryCountPoCbatchesAtHeightRequest) (*types.QueryCountPoCbatchesAtHeightResponse, error) {
	if req == nil {
        return nil, status.Error(codes.InvalidArgument, "invalid request")
    }

	ctx := sdk.UnwrapSDKContext(goCtx)

    // Get all PoCBatches at the specified height
    pocBatches, err := k.GetPoCBatchesByStage(ctx, req.BlockHeight)
    if err != nil {
        return nil, status.Error(codes.Internal, err.Error())
    }

    // Count the total number of PoCBatches
    var count uint64 = 0
    for _, batches := range pocBatches {
        count += uint64(len(batches))
    }

	return &types.QueryCountPoCbatchesAtHeightResponse{
        Count: count,
    }, nil
}
