package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochPerformanceSummaryAll(ctx context.Context, req *types.QueryAllEpochPerformanceSummaryRequest) (*types.QueryAllEpochPerformanceSummaryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochPerformanceSummarys, pageRes, err := query.CollectionPaginate[collections.Pair[sdk.AccAddress, uint64], types.EpochPerformanceSummary](
		ctx,
		k.EpochPerformanceSummaries,
		req.Pagination,
		func(_ collections.Pair[sdk.AccAddress, uint64], v types.EpochPerformanceSummary) (types.EpochPerformanceSummary, error) {
			return v, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryAllEpochPerformanceSummaryResponse{EpochPerformanceSummary: epochPerformanceSummarys, Pagination: pageRes}, nil
}

func (k Keeper) EpochPerformanceSummary(ctx context.Context, req *types.QueryGetEpochPerformanceSummaryRequest) (*types.QueryGetEpochPerformanceSummaryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetEpochPerformanceSummary(
		ctx,
		req.EpochIndex,
		req.ParticipantId,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetEpochPerformanceSummaryResponse{EpochPerformanceSummary: val}, nil
}
