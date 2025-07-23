package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) CountPoCvalidationsAtHeight(ctx context.Context, req *types.QueryCountPoCvalidationsAtHeightRequest) (*types.QueryCountPoCvalidationsAtHeightResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	count, err := k.CountPoCValidationsAtHeight(ctx, req.BlockHeight)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryCountPoCvalidationsAtHeightResponse{
		Count: count,
	}, nil
}
