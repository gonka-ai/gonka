package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// GetEpochModel retrieves the model snapshot for a given model ID from the current epoch's data.
func (k Keeper) GetEpochModel(ctx context.Context, modelId string) (*types.Model, error) {
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, types.ErrEffectiveEpochNotFound
	}
	return k.GetEpochModelForEpoch(ctx, effectiveEpochIndex, modelId)
}

// GetEpochModelForEpoch retrieves the model snapshot for a given model ID from a specific epoch.
func (k Keeper) GetEpochModelForEpoch(ctx context.Context, epochId uint64, modelId string) (*types.Model, error) {
	epochGroup, err := k.GetEpochGroup(ctx, epochId, modelId)
	if err != nil {
		return nil, err
	}

	if epochGroup.GroupData == nil || epochGroup.GroupData.ModelSnapshot == nil {
		return nil, types.ErrModelSnapshotNotFound
	}

	return epochGroup.GroupData.ModelSnapshot, nil
}
