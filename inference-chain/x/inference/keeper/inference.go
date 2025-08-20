package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// SetInference set a specific inference in the store from its index
func (k Keeper) SetInference(ctx context.Context, inference types.Inference) {
	// store via collections
	if err := k.Inferences.Set(ctx, inference.Index, inference); err != nil {
		panic(err)
	}

	err := k.SetDeveloperStats(ctx, inference)
	if err != nil {
		k.LogError("error setting developer stat", types.Stat, "err", err)
	} else {
		k.LogInfo("updated developer stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy)
	}
}

func (k Keeper) SetInferenceWithoutDevStatComputation(ctx context.Context, inference types.Inference) {
	if err := k.Inferences.Set(ctx, inference.Index, inference); err != nil {
		panic(err)
	}
}

// GetInference returns a inference from its index
func (k Keeper) GetInference(
	ctx context.Context,
	index string,

) (val types.Inference, found bool) {
	v, err := k.Inferences.Get(ctx, index)
	if err != nil {
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
func (k Keeper) GetAllInference(ctx context.Context) (list []types.Inference) {
	iter, err := k.Inferences.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	defer iter.Close()
	vals, err := iter.Values()
	if err != nil {
		return nil
	}
	return vals
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
