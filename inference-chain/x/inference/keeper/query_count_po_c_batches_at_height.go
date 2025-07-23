package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) CountPoCbatchesAtHeight(ctx context.Context, req *types.QueryCountPoCbatchesAtHeightRequest) (*types.QueryCountPoCbatchesAtHeightResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	count, err := k.CountPoCBatchesAtHeight(ctx, req.BlockHeight)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryCountPoCbatchesAtHeightResponse{
		Count: count,
	}, nil
}
