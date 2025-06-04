package keeper

import (
	"context"
	"errors"
	"github.com/productscience/inference/x/inference/types"
)

var (
	ErrInvalidDeveloperAddress = errors.New("invalid developer address")
	ErrInvalidTimePeriod       = errors.New("invalid time period")
)

func (k Keeper) StatsByTimePeriodByDeveloper(ctx context.Context, req *types.QueryStatsByTimePeriodByDeveloperRequest) (*types.QueryStatsByTimePeriodByDeveloperResponse, error) {
	if req.Developer == "" {
		return nil, ErrInvalidDeveloperAddress
	}

	if req.TimeTo <= req.TimeFrom {
		return nil, ErrInvalidTimePeriod
	}
	stats := k.DevelopersStatsGetByTime(ctx, req.Developer, req.TimeTo, req.TimeFrom)
	return &types.QueryStatsByTimePeriodByDeveloperResponse{Stats: stats}, nil
}

func (k Keeper) StatsByDeveloperAndEpochsBackwards(ctx context.Context, req *types.QueryStatsByDeveloperAndEpochBackwardsRequest) (*types.QueryInferencesAndTokensStatsResponse, error) {
	if req.Developer == "" {
		return nil, ErrInvalidDeveloperAddress
	}

	tokens, inferences := k.CountTotalInferenceInLastNEpochsByDeveloper(ctx, req.Developer, int(req.EpochsN))
	return &types.QueryInferencesAndTokensStatsResponse{
		AiTokens:   tokens,
		Inferences: int32(inferences),
	}, nil
}

func (k Keeper) InferencesAndTokensStatsByEpochsBackwards(ctx context.Context, req *types.QueryInferencesAndTokensStatsByEpochsBackwardsRequest) (*types.QueryInferencesAndTokensStatsResponse, error) {
	tokens, inferences := k.CountTotalInferenceInLastNEpochs(ctx, int(req.EpochN))
	return &types.QueryInferencesAndTokensStatsResponse{AiTokens: tokens, Inferences: int32(inferences)}, nil
}

func (k Keeper) InferencesAndTokensStatsByTimePeriod(ctx context.Context, req *types.QueryInferencesAndTokensStatsByTimePeriodRequest) (*types.QueryInferencesAndTokensStatsResponse, error) {
	if req.TimeTo <= req.TimeFrom {
		return nil, ErrInvalidTimePeriod
	}

	tokens, inferences := k.CountTotalInferenceInPeriod(ctx, req.TimeTo, req.TimeFrom)
	return &types.QueryInferencesAndTokensStatsResponse{AiTokens: tokens, Inferences: int32(inferences)}, nil
}
