package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetCurrentEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	currentId, found := k.GetEffectiveEpochPocStartHeight(ctx)
	return k.GetOrCreateEpochGroup(ctx, currentId, "")
}

func (k Keeper) GetCurrentEpochGroupOrNil(ctx context.Context) (*epochgroup.EpochGroup, error) {
	currentId, found := k.GetEffectiveEpochPocStartHeight(ctx)
	if currentId == 0 {
		return nil, nil
	} else {
		return k.GetOrCreateEpochGroup(ctx, currentId, "")
	}
}

func (k Keeper) GetUpcomingEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	currentId := k.GetUpcomingEpochPocStartHeight(ctx)
	return k.GetOrCreateEpochGroup(ctx, currentId, "")
}

func (k Keeper) GetPreviousEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if effectiveEpochIndex == 0 {
		return nil, false
	}
	upcomingEpoch, found := k.GetEpoch(ctx, effectiveEpochIndex+1)
	if !found {
		return nil, false
	}

	return k.GetOrCreateEpochGroup(ctx, uint64(upcomingEpoch.PocStartBlockHeight), "")
}

func (k Keeper) GetOrCreateEpochGroup(ctx context.Context, pocStartHeight uint64, modelId string) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, pocStartHeight, modelId)
	if !found {
		data = types.EpochGroupData{
			PocStartBlockHeight: pocStartHeight,
			ModelId:             modelId,
		}
		k.SetEpochGroupData(ctx, data)
	}

	return epochgroup.NewEpochGroup(
		k.group,
		k,
		k.GetAuthority(),
		k,
		k,
		&data,
	), nil
}
