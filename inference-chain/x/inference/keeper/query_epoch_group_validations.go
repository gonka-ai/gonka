package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochGroupValidationsAll(ctx context.Context, req *types.QueryAllEpochGroupValidationsRequest) (*types.QueryAllEpochGroupValidationsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochGroupValidations, pageRes, err := query.CollectionPaginate(
		ctx,
		k.EpochGroupValidationsMap,
		req.Pagination,
		func(_ collections.Pair[uint64, string], v types.EpochGroupValidations) (types.EpochGroupValidations, error) {
			return v, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllEpochGroupValidationsResponse{EpochGroupValidations: epochGroupValidations, Pagination: pageRes}, nil
}

func (k Keeper) EpochGroupValidations(ctx context.Context, req *types.QueryGetEpochGroupValidationsRequest) (*types.QueryGetEpochGroupValidationsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetEpochGroupValidations(
		ctx,
		req.Participant,
		req.EpochIndex,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetEpochGroupValidationsResponse{EpochGroupValidations: val}, nil
}
