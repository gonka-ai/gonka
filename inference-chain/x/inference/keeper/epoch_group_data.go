package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupData set a specific epochGroupData in the store from its index
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	if err := k.EpochGroupDataMap.Set(ctx, collections.Join(epochGroupData.EpochIndex, epochGroupData.ModelId), epochGroupData); err != nil {
		k.LogError("SetEpochGroupData: failed to persist epoch group data", types.EpochGroup, "epochIndex", epochGroupData.EpochIndex, "modelId", epochGroupData.ModelId, "error", err)
	}
}

// GetEpochGroupData returns a epochGroupData from its index
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	val, err := k.EpochGroupDataMap.Get(ctx, collections.Join(epochIndex, modelId))

	if err != nil {
		return val, false
	}
	return val, true
}

// RemoveEpochGroupData removes a epochGroupData from the store
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	if err := k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId)); err != nil {
		k.LogError("RemoveEpochGroupData: failed to delete epoch group data", types.EpochGroup, "epochIndex", epochIndex, "modelId", modelId, "error", err)
	}
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
