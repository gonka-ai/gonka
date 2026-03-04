package keeper

import (
	"context"

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

// GetEpochGroupDataForEpoch returns all epochGroupData entries for a specific epoch index.
// This is more efficient than GetAllEpochGroupData when only data for one epoch is needed,
// as it avoids loading epoch group data from all historical epochs.
func (k Keeper) GetEpochGroupDataForEpoch(ctx context.Context, epochIndex uint64) (list []types.EpochGroupData) {
	iter, err := k.EpochGroupDataMap.Iterate(ctx, collections.NewPrefixedPairRange[uint64, string](epochIndex))
	if err != nil {
		return nil
	}
	defer iter.Close()
	epochGroupDataList, err := iter.Values()
	if err != nil {
		return nil
	}
	return epochGroupDataList
}
