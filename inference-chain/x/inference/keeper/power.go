package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetCurrentEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	effectiveEpochPocStartHeight, found := k.GetEffectiveEpochPocStartHeight(ctx)
	if !found {
		return nil, types.ErrEffectiveEpochNotFound
	}

	return k.GetEpochGroup(ctx, effectiveEpochPocStartHeight, "")
}

func (k Keeper) GetUpcomingEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	upcomingEpochPocStartHeight, found := k.GetUpcomingEpochPocStartHeight(ctx)
	if !found {
		return nil, types.ErrUpcomingEpochNotFound
	}

	return k.GetEpochGroup(ctx, upcomingEpochPocStartHeight, "")
}

func (k Keeper) GetPreviousEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	previousEpochPocStartHeight, found := k.GetPreviousEpochPocStartHeight(ctx)
	if !found {
		return nil, types.ErrPreviousEpochNotFound
	}

	return k.GetEpochGroup(ctx, previousEpochPocStartHeight, "")
}

func (k Keeper) GetEpochGroupForEpoch(ctx context.Context, epoch types.Epoch) (*epochgroup.EpochGroup, error) {
	return k.GetEpochGroup(ctx, uint64(epoch.PocStartBlockHeight), "")
}

func (k Keeper) GetEpochGroup(ctx context.Context, pocStartHeight uint64, modelId string) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, pocStartHeight, modelId)
	if !found {
		return nil, types.ErrEpochGroupDataNotFound
	}

	return k.epochGroupFromData(data), nil
}

func (k Keeper) CreateEpochGroup(ctx context.Context, pocStartHeight uint64, epochId uint64) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, pocStartHeight, "")
	if found {
		k.LogError("CreateEpochGroup: Root epoch group data already exists", types.EpochGroup, "pocStartHeight", pocStartHeight)
		return nil, types.ErrEpochGroupDataAlreadyExists
	} else {
		data = types.EpochGroupData{
			PocStartBlockHeight: pocStartHeight,
			ModelId:             "",
			EpochId:             epochId,
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
