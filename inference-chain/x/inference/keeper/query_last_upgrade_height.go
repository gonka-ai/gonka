package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) LastUpgradeHeight(ctx context.Context, req *types.QueryGetLastUpgradeHeightRequest) (*types.QueryGetLastUpgradeHeightResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	height, found := k.GetLastUpgradeHeight(ctx)

	return &types.QueryGetLastUpgradeHeightResponse{
		LastUpgradeHeight: height,
		Found:             found,
	}, nil
}
