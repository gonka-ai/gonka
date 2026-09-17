package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupData set a specific epochGroupData in the store from its index
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	k.EpochGroupDataMap.Set(ctx, collections.Join(epochGroupData.EpochIndex, epochGroupData.ModelId), epochGroupData)
}

// GetEpochGroupData returns a epochGroupData from its index
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	val, found, _ = k.GetEpochGroupDataWithError(ctx, epochIndex, modelId)
	return val, found
}

// GetEpochGroupDataWithError distinguishes a missing record from a store error.
func (k Keeper) GetEpochGroupDataWithError(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool, err error) {
	val, err = k.EpochGroupDataMap.Get(ctx, collections.Join(epochIndex, modelId))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return val, false, nil
		}
		return val, false, err
	}
	return val, true, nil
}

// RemoveEpochGroupData removes a epochGroupData from the store
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId))
}

// GetAllEpochGroupData returns all epochGroupData
func (k Keeper) GetAllEpochGroupData(ctx context.Context) (list []types.EpochGroupData) {
	iter, err := k.EpochGroupDataMap.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	epochGroupDataList, err := iter.Values()
	if err != nil {
		return nil
	}
	return epochGroupDataList
}
