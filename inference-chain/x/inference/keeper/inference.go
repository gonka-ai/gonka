package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetInference set a specific inference in the store from its index
func (k Keeper) SetInference(ctx context.Context, inference types.Inference) error {
	// store via collections
	k.addInferenceToPruningList(ctx, inference)
	if err := k.Inferences.Set(ctx, inference.Index, inference); err != nil {
		return err
	}

	err := k.SetDeveloperStats(ctx, inference)
	if err != nil {
		k.LogError("error setting developer stat", types.Stat, "err", err)
	} else {
		k.LogInfo("updated developer stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy)
	}
	return nil
}

func (k Keeper) SetInferenceWithoutDevStatComputation(ctx context.Context, inference types.Inference) error {
	k.addInferenceToPruningList(ctx, inference)
	return k.Inferences.Set(ctx, inference.Index, inference)
}

func (k Keeper) addInferenceToPruningList(ctx context.Context, inference types.Inference) {
	if inference.EpochId != 0 {
		key := collections.Join(int64(inference.EpochId), inference.Index)
		err := k.InferencesToPrune.Set(ctx, key, collections.NoValue{})
		if err != nil {
			k.LogError("Unable to set InferencesToPrune", types.Pruning, "error", err, "key", key)
		}
	}
}

// GetInference returns a inference from its index
func (k Keeper) GetInference(
	ctx context.Context,
	index string,

) (val types.Inference, found bool) {
	v, err := k.Inferences.Get(ctx, index)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			k.LogError("Unexpected error retrieving inference", types.Inferences, "error", err, "index", index)
		}
		return val, false
	}
	return v, true
}

// RemoveInference removes a inference from the store
func (k Keeper) RemoveInference(
	ctx context.Context,
	index string,

) {
	_ = k.Inferences.Remove(ctx, index)
}

// GetAllInference returns all inference
func (k Keeper) GetAllInference(ctx context.Context) (list []types.Inference, err error) {
	iter, err := k.Inferences.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	vals, err := iter.Values()
	if err != nil {
		return nil, err
	}
	return vals, nil
}

func (k Keeper) GetInferences(ctx context.Context, ids []string) ([]types.Inference, bool) {
	result := make([]types.Inference, len(ids))
	for i, id := range ids {
		v, err := k.Inferences.Get(ctx, id)
		if err != nil {
			return nil, false
		}
		result[i] = v
	}
	return result, true
}
