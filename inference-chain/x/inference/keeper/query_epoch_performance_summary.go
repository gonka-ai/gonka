package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochPerformanceSummaryAll(ctx context.Context, req *types.QueryAllEpochPerformanceSummaryRequest) (*types.QueryAllEpochPerformanceSummaryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var epochPerformanceSummarys []types.EpochPerformanceSummary

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochPerformanceSummaryStore := prefix.NewStore(store, types.KeyPrefix(types.EpochPerformanceSummaryKeyPrefix))

	pageRes, err := query.Paginate(epochPerformanceSummaryStore, req.Pagination, func(key []byte, value []byte) error {
		var epochPerformanceSummary types.EpochPerformanceSummary
		if err := k.cdc.Unmarshal(value, &epochPerformanceSummary); err != nil {
			return err
		}

		epochPerformanceSummarys = append(epochPerformanceSummarys, epochPerformanceSummary)
		return nil
	})

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
		req.EpochStartHeight,
		req.ParticipantId,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetEpochPerformanceSummaryResponse{EpochPerformanceSummary: val}, nil
}

func (k Keeper) EpochPerformanceSummaryByParticipants(ctx context.Context, req *types.QueryParticipantsEpochPerformanceSummaryRequest) (*types.QueryParticipantsEpochPerformanceSummaryResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val := k.GetParticipantsEpochSummaries(
		ctx,
		req.ParticipantId,
		req.EpochStartHeight,
	)
	return &types.QueryParticipantsEpochPerformanceSummaryResponse{EpochPerformanceSummary: val}, nil
}
