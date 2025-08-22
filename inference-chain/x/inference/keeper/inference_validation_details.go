package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetInferenceValidationDetails sets a specific InferenceValidationDetails value keyed by (epochId, inferenceId)
func (k Keeper) SetInferenceValidationDetails(ctx context.Context, inferenceValidationDetails types.InferenceValidationDetails) {
	_ = k.InferenceValidationDetailsMap.Set(ctx,
		collections.Join(inferenceValidationDetails.EpochId, inferenceValidationDetails.InferenceId),
		inferenceValidationDetails,
	)
}

// GetInferenceValidationDetails returns an InferenceValidationDetails by (epochId, inferenceId)
func (k Keeper) GetInferenceValidationDetails(
	ctx context.Context,
	epochId uint64,
	inferenceId string,
) (val types.InferenceValidationDetails, found bool) {
	v, err := k.InferenceValidationDetailsMap.Get(ctx, collections.Join(epochId, inferenceId))
	if err != nil {
		return types.InferenceValidationDetails{}, false
	}
	return v, true
}

// RemoveInferenceValidationDetails removes an InferenceValidationDetails by (epochId, inferenceId)
func (k Keeper) RemoveInferenceValidationDetails(
	ctx context.Context,
	epochId uint64,
	inferenceId string,
) {
	_ = k.InferenceValidationDetailsMap.Remove(ctx, collections.Join(epochId, inferenceId))
}

// GetInferenceValidationDetailsForEpoch returns all InferenceValidationDetails for a given epochId
func (k Keeper) GetInferenceValidationDetailsForEpoch(ctx context.Context, epochId uint64) (list []types.InferenceValidationDetails) {
	it, err := k.InferenceValidationDetailsMap.Iterate(ctx, collections.NewPrefixedPairRange[uint64, string](epochId))
	if err != nil {
		return nil
	}
	defer it.Close()
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			return nil
		}
		list = append(list, v)
	}
	return list
}

// GetAllInferenceValidationDetails returns all InferenceValidationDetails
func (k Keeper) GetAllInferenceValidationDetails(ctx context.Context) (list []types.InferenceValidationDetails) {
	it, err := k.InferenceValidationDetailsMap.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	defer it.Close()
	vals, err := it.Values()
	if err != nil {
		return nil
	}
	return vals
}
