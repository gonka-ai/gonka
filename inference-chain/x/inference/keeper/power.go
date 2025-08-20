package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetCurrentEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, types.ErrEffectiveEpochNotFound
	}

	return k.GetEpochGroup(ctx, effectiveEpochIndex, "")
}

func (k Keeper) GetUpcomingEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	upcomingEpochIndex, found := k.GetUpcomingEpochIndex(ctx)
	if !found {
		return nil, types.ErrUpcomingEpochNotFound
	}

	return k.GetEpochGroup(ctx, upcomingEpochIndex, "")
}

func (k Keeper) GetPreviousEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	previousEpochIndex, found := k.GetPreviousEpochIndex(ctx)
	if !found {
		return nil, types.ErrPreviousEpochNotFound
	}

	return k.GetEpochGroup(ctx, previousEpochIndex, "")
}

func (k Keeper) GetEpochGroupForEpoch(ctx context.Context, epoch types.Epoch) (*epochgroup.EpochGroup, error) {
	return k.GetEpochGroup(ctx, epoch.Index, "")
}

func (k Keeper) GetEpochGroup(ctx context.Context, epochIndex uint64, modelId string) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, epochIndex, modelId)
	if !found {
		return nil, types.ErrEpochGroupDataNotFound
	}

	return k.epochGroupFromData(data), nil
}

func (k Keeper) CreateEpochGroup(ctx context.Context, pocStartHeight uint64, epochIndex uint64) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, epochIndex, "")
	if found {
		k.LogError("CreateEpochGroup: Root epoch group data already exists", types.EpochGroup, "epochIndex", epochIndex)
		return nil, types.ErrEpochGroupDataAlreadyExists
	} else {
		data = types.EpochGroupData{
			PocStartBlockHeight: pocStartHeight,
			ModelId:             "",
			EpochIndex:          epochIndex,
		}
		k.SetEpochGroupData(ctx, data)
	}

	return k.epochGroupFromData(data), nil
}

func (k Keeper) epochGroupFromData(data types.EpochGroupData) *epochgroup.EpochGroup {
	return epochgroup.NewEpochGroup(
		k.group,
		k,
		k,
		k,
		k.GetAuthority(),
		k,
		k,
		&data,
	)
}
