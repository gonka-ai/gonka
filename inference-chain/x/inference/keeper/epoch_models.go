package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// GetEpochModel retrieves the model snapshot for a given model ID from the current epoch's data.
func (k Keeper) GetEpochModel(ctx context.Context, modelId string) (*types.Model, error) {
	currentGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, err
	}

	// Get the sub-group for the specified model.
	// The sub-group contains the model snapshot.
	modelSubGroup, err := currentGroup.GetSubGroup(ctx, modelId)
	if err != nil {
		return nil, err
	}

	if modelSubGroup.GroupData == nil || modelSubGroup.GroupData.ModelSnapshot == nil {
		return nil, types.ErrModelSnapshotNotFound
	}

	return modelSubGroup.GroupData.ModelSnapshot, nil
}
