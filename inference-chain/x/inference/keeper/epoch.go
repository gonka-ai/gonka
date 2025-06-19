package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetEpoch(ctx context.Context, epoch types.Epoch) {
	SetValue(k, ctx, &epoch, types.KeyPrefix(types.EpochKeyPrefix), types.EpochKey(epoch.Index))
}

func (k Keeper) GetEpoch(ctx context.Context, epochIndex uint64) (*types.Epoch, bool) {
	epoch := types.Epoch{}
	keyPrefix := types.KeyPrefix(types.EpochKeyPrefix)
	key := types.EpochKey(epochIndex)
	return GetValue(&k, ctx, &epoch, keyPrefix, key)
}
