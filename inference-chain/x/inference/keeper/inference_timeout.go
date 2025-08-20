package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetInferenceTimeout sets a specific inferenceTimeout into the collections map
func (k Keeper) SetInferenceTimeout(ctx context.Context, inferenceTimeout types.InferenceTimeout) {
	_ = k.InferenceTimeouts.Set(ctx, collections.Join(inferenceTimeout.ExpirationHeight, inferenceTimeout.InferenceId), inferenceTimeout)
}

// GetInferenceTimeout returns an inferenceTimeout from its composite key (expirationHeight, inferenceId)
func (k Keeper) GetInferenceTimeout(
	ctx context.Context,
	expirationHeight uint64,
	inferenceId string,
) (val types.InferenceTimeout, found bool) {
	v, err := k.InferenceTimeouts.Get(ctx, collections.Join(expirationHeight, inferenceId))
	if err != nil {
		return val, false
	}
	return v, true
}

// RemoveInferenceTimeout removes an inferenceTimeout from collections
func (k Keeper) RemoveInferenceTimeout(
	ctx context.Context,
	expirationHeight uint64,
	inferenceId string,
) {
	_ = k.InferenceTimeouts.Remove(ctx, collections.Join(expirationHeight, inferenceId))
}

// GetAllInferenceTimeout returns all inferenceTimeout entries deterministically
func (k Keeper) GetAllInferenceTimeout(ctx context.Context) (list []types.InferenceTimeout) {
	iter, err := k.InferenceTimeouts.Iterate(ctx, nil)
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

// GetAllInferenceTimeoutForHeight returns all inferenceTimeouts for a given expirationHeight
func (k Keeper) GetAllInferenceTimeoutForHeight(ctx context.Context, expirationHeight uint64) (list []types.InferenceTimeout) {
	it, err := k.InferenceTimeouts.Iterate(ctx, collections.NewPrefixedPairRange[uint64, string](expirationHeight))
	if err != nil {
		return nil
	}
	defer it.Close()
	var out []types.InferenceTimeout
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}
