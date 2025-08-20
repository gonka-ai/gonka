package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochGroupDataAll(ctx context.Context, req *types.QueryAllEpochGroupDataRequest) (*types.QueryAllEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochGroupDatas, pageRes, err := query.CollectionPaginate(
		ctx,
		k.EpochGroupDataMap,
		req.Pagination,
		func(_ collections.Pair[uint64, string], value types.EpochGroupData) (types.EpochGroupData, error) {
			return value, nil
		},
	)

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllEpochGroupDataResponse{EpochGroupData: epochGroupDatas, Pagination: pageRes}, nil
}

func (k Keeper) EpochGroupData(ctx context.Context, req *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetEpochGroupData(
		ctx,
		req.EpochIndex,
		req.ModelId,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: val}, nil
}
