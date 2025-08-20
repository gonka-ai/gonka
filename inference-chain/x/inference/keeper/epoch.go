package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetEffectiveEpochIndex(ctx context.Context, epoch uint64) {
	if err := k.EffectiveEpochIndex.Set(ctx, epoch); err != nil {
		panic(err)
	}
}

func (k Keeper) GetEffectiveEpochIndex(ctx context.Context) (uint64, bool) {
	v, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (k Keeper) SetEpoch(ctx context.Context, epoch *types.Epoch) {
	if epoch == nil {
		k.LogError("SetEpoch called with nil epoch, returning", types.System)
		return
	}
	if err := k.Epochs.Set(ctx, epoch.Index, *epoch); err != nil {
		panic(err)
	}
}

func (k Keeper) GetEpoch(ctx context.Context, epochIndex uint64) (*types.Epoch, bool) {
	v, err := k.Epochs.Get(ctx, epochIndex)
	if err != nil {
		return nil, false
	}
	return &v, true
}

func (k Keeper) GetEffectiveEpoch(ctx context.Context) (*types.Epoch, bool) {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("GetEffectiveEpochIndex returned false, no effective epoch found", types.EpochGroup)
		return nil, false
	}
	return k.GetEpoch(ctx, epochIndex)
}

func (k Keeper) GetUpcomingEpoch(ctx context.Context) (*types.Epoch, bool) {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, false
	}

	return k.GetEpoch(ctx, epochIndex+1)
}

// GetLatestEpoch return upcoming epoch if it exists (PoC stage already started),
//
//	otherwise return effective epoch (next PoC stage not started yet).
func (k Keeper) GetLatestEpoch(ctx context.Context) (*types.Epoch, bool) {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, false
	}

	upcomingEpoch, found := k.GetEpoch(ctx, epochIndex+1)
	if found && upcomingEpoch != nil {
		return upcomingEpoch, true
	}

	return k.GetEpoch(ctx, epochIndex)
}

func (k Keeper) GetPreviousEpoch(ctx context.Context) (*types.Epoch, bool) {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found || epochIndex == 0 {
		return nil, false
	}

	return k.GetEpoch(ctx, epochIndex-1)
}

func (k Keeper) GetEffectiveEpochPocStartHeight(ctx context.Context) (uint64, bool) {
	epoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		return 0, false
	}

	return uint64(epoch.PocStartBlockHeight), true
}

func (k Keeper) GetUpcomingEpochIndex(ctx context.Context) (uint64, bool) {
	epoch, found := k.GetUpcomingEpoch(ctx)
	if !found {
		return 0, false
	}
	return epoch.Index, true
}

func (k Keeper) GetUpcomingEpochPocStartHeight(ctx context.Context) (uint64, bool) {
	epoch, found := k.GetUpcomingEpoch(ctx)
	if !found {
		return 0, false
	}

	return uint64(epoch.PocStartBlockHeight), true
}

func (k Keeper) GetPreviousEpochIndex(ctx context.Context) (uint64, bool) {
	epoch, found := k.GetPreviousEpoch(ctx)
	if !found {
		return 0, false
	}
	return epoch.Index, true
}

func (k Keeper) GetPreviousEpochPocStartHeight(ctx context.Context) (uint64, bool) {
	epoch, found := k.GetPreviousEpoch(ctx)
	if !found {
		return 0, false
	}

	return uint64(epoch.PocStartBlockHeight), true
}
