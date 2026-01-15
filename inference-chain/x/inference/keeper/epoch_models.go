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
	epochGroup, err := k.GetEpochGroup(ctx, epochId, "")
	if err != nil {
		return nil, err
	}

	// Get the sub-group for the specified model.
	// The sub-group contains the model snapshot.
	modelSubGroup, err := epochGroup.GetSubGroup(ctx, modelId)
	if err != nil {
		return nil, err
	}

	if modelSubGroup.GroupData == nil || modelSubGroup.GroupData.ModelSnapshot == nil {
		return nil, types.ErrModelSnapshotNotFound
	}

	return modelSubGroup.GroupData.ModelSnapshot, nil
}
