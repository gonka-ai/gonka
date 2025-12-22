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

// EpochPerformanceSummary returns all summaries for a given epoch index.
func (k Keeper) EpochPerformanceSummary(ctx context.Context, req *types.QueryEpochPerformanceSummaryByEpochRequest) (*types.QueryEpochPerformanceSummaryByEpochResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	all := k.GetAllEpochPerformanceSummary(ctx)
	var filtered []types.EpochPerformanceSummary
	for _, s := range all {
		if s.EpochIndex == req.EpochIndex {
			filtered = append(filtered, s)
		}
	}

	return &types.QueryEpochPerformanceSummaryByEpochResponse{EpochPerformanceSummary: filtered}, nil
}

// EpochPerformanceSummaryByParticipant returns a single summary for a given epoch index and participant.
func (k Keeper) EpochPerformanceSummaryByParticipant(ctx context.Context, req *types.QueryEpochPerformanceSummaryByParticipantRequest) (*types.QueryEpochPerformanceSummaryByParticipantResponse, error) {
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

	return &types.QueryEpochPerformanceSummaryByParticipantResponse{EpochPerformanceSummary: val}, nil
}
