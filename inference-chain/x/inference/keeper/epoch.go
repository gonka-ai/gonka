package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetEpoch(ctx context.Context, epoch *types.Epoch) {
	if epoch == nil {
		k.LogError("SetEpoch called with nil epoch, returning", types.System)
		return
	}

	SetValue(k, ctx, epoch, types.KeyPrefix(types.EpochKeyPrefix), types.EpochKey(epoch.Index))
}

func (k Keeper) GetEpoch(ctx context.Context, epochIndex uint64) (*types.Epoch, bool) {
	epoch := types.Epoch{}
	keyPrefix := types.KeyPrefix(types.EpochKeyPrefix)
	key := types.EpochKey(epochIndex)
	return GetValue(&k, ctx, &epoch, keyPrefix, key)
}

func (k Keeper) GetEffectiveEpoch(ctx context.Context) (*types.Epoch, bool) {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, found
	}
	return k.GetEpoch(ctx, epochIndex)
}

func (k Keeper) GetUpcomingEpoch(ctx context.Context) (*types.Epoch, bool) {
	epochIndex, found := k.GetUpcomingEpochIndex(ctx)
	if !found {
		return nil, found
	}
	return k.GetEpoch(ctx, epochIndex)
}

func (k Keeper) GetPreviousEpoch(ctx context.Context) (*types.Epoch, bool) {
	epochIndex, found := k.GetPreviousEpochIndex(ctx)
	if !found {
		return nil, found
	}
	return k.GetEpoch(ctx, epochIndex)
}
