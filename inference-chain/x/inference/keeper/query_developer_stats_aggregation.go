package keeper

import (
	"context"
	"errors"
	"github.com/productscience/inference/x/inference/types"
	"time"
)

var (
	ErrInvalidDeveloperAddress = errors.New("invalid developer address")
	ErrInvalidTimePeriod       = errors.New("invalid time period")
)

const defaultTimePeriod = time.Hour * -24

func (k Keeper) StatsByTimePeriodByDeveloper(ctx context.Context, req *types.QueryStatsByTimePeriodByDeveloperRequest) (*types.QueryStatsByTimePeriodByDeveloperResponse, error) {
	if req.Developer == "" {
		return nil, ErrInvalidDeveloperAddress
	}

	if req.TimeTo < req.TimeFrom {
		return nil, ErrInvalidTimePeriod
	}

	if req.TimeTo == 0 {
		req.TimeTo = time.Now().UnixMilli()
	}

	if req.TimeFrom == 0 {
		req.TimeFrom = time.Now().Add(defaultTimePeriod).UnixMilli()
	}

	k.LogInfo("StatsByTimePeriodByDeveloper", types.Stat, "developer", req.Developer, "time_from", req.TimeFrom, "time_to", req.TimeTo)
	stats := k.GetDeveloperStatsByTime(ctx, req.Developer, req.TimeFrom, req.TimeTo)
	return &types.QueryStatsByTimePeriodByDeveloperResponse{Stats: stats}, nil
}

func (k Keeper) StatsByDeveloperAndEpochsBackwards(ctx context.Context, req *types.QueryStatsByDeveloperAndEpochBackwardsRequest) (*types.QueryInferencesAndTokensStatsResponse, error) {
	if req.Developer == "" {
		return nil, ErrInvalidDeveloperAddress
	}

	summary := k.GetSummaryLastNEpochsByDeveloper(ctx, req.Developer, int(req.EpochsN))
	return &types.QueryInferencesAndTokensStatsResponse{
		AiTokens:             summary.TokensUsed,
		Inferences:           int32(summary.InferenceCount),
		ActualInferencesCost: summary.ActualCost}, nil
}

func (k Keeper) InferencesAndTokensStatsByEpochsBackwards(ctx context.Context, req *types.QueryInferencesAndTokensStatsByEpochsBackwardsRequest) (*types.QueryInferencesAndTokensStatsResponse, error) {
	summary := k.GetSummaryLastNEpochs(ctx, int(req.EpochsN))
	return &types.QueryInferencesAndTokensStatsResponse{
		AiTokens:             summary.TokensUsed,
		Inferences:           int32(summary.InferenceCount),
		ActualInferencesCost: summary.ActualCost}, nil
}

func (k Keeper) InferencesAndTokensStatsByTimePeriod(ctx context.Context, req *types.QueryInferencesAndTokensStatsByTimePeriodRequest) (*types.QueryInferencesAndTokensStatsResponse, error) {
	if req.TimeTo < req.TimeFrom {
		return nil, ErrInvalidTimePeriod
	}

	if req.TimeTo == 0 {
		req.TimeTo = time.Now().UnixMilli()
	}

	if req.TimeFrom == 0 {
		req.TimeFrom = time.Now().Add(defaultTimePeriod).UnixMilli()
	}

	k.LogInfo("InferencesAndTokensStatsByTimePeriod", types.Stat, "time_from", req.TimeFrom, "time_to", req.TimeTo)
	summary := k.GetSummaryByTime(ctx, req.TimeFrom, req.TimeTo)
	return &types.QueryInferencesAndTokensStatsResponse{
		AiTokens:             summary.TokensUsed,
		Inferences:           int32(summary.InferenceCount),
		ActualInferencesCost: summary.ActualCost,
	}, nil
}

func (k Keeper) InferencesAndTokensStatsByModels(ctx context.Context, req *types.QueryInferencesAndTokensStatsByModelsRequest) (*types.QueryInferencesAndTokensStatsByModelsResponse, error) {
	if req.TimeTo < req.TimeFrom {
		return nil, ErrInvalidTimePeriod
	}

	if req.TimeTo == 0 {
		req.TimeTo = time.Now().UnixMilli()
	}

	if req.TimeFrom == 0 {
		req.TimeFrom = time.Now().Add(defaultTimePeriod).UnixMilli()
	}

	stats := make([]*types.ModelStats, 0)
	statsPerModels := k.GetSummaryByModelAndTime(ctx, req.TimeFrom, req.TimeTo)
	for modelName, summary := range statsPerModels {
		stats = append(stats, &types.ModelStats{
			Model:      modelName,
			AiTokens:   summary.TokensUsed,
			Inferences: int32(summary.InferenceCount),
		})
	}
	return &types.QueryInferencesAndTokensStatsByModelsResponse{StatsModels: stats}, nil
}

func (k Keeper) DebugStatsDeveloperStats(ctx context.Context, _ *types.QueryDebugStatsRequest) (*types.QueryDebugStatsResponse, error) {
	statByEpoch, statByTime := k.DumpAllDeveloperStats(ctx)

	resp := &types.QueryDebugStatsResponse{
		StatsByTime:  make([]*types.QueryDebugStatsResponse_TemporaryTimeStat, 0),
		StatsByEpoch: make([]*types.QueryDebugStatsResponse_TemporaryEpochStat, 0),
	}

	for developer, stat := range statByTime {
		resp.StatsByTime = append(resp.StatsByTime, &types.QueryDebugStatsResponse_TemporaryTimeStat{
			Developer: developer,
			Stats:     stat,
		})
	}

	for developer, stat := range statByEpoch {
		resp.StatsByEpoch = append(resp.StatsByEpoch, &types.QueryDebugStatsResponse_TemporaryEpochStat{
			Developer: developer,
			Stats:     stat,
		})
	}
	return resp, nil
}
