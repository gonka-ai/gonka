package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PartialUpgradeAll(ctx context.Context, req *types.QueryAllPartialUpgradeRequest) (*types.QueryAllPartialUpgradeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	partialUpgrades, pageRes, err := query.CollectionPaginate(
		ctx,
		k.PartialUpgrades,
		req.Pagination,
		func(_ uint64, v types.PartialUpgrade) (types.PartialUpgrade, error) { return v, nil },
	)

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllPartialUpgradeResponse{PartialUpgrade: partialUpgrades, Pagination: pageRes}, nil
}

func (k Keeper) PartialUpgrade(ctx context.Context, req *types.QueryGetPartialUpgradeRequest) (*types.QueryGetPartialUpgradeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetPartialUpgrade(
		ctx,
		req.Height,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetPartialUpgradeResponse{PartialUpgrade: val}, nil
}
