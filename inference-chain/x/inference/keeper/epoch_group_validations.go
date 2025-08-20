package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupValidations set a specific epochGroupValidations in the store from its index
func (k Keeper) SetEpochGroupValidations(ctx context.Context, epochGroupValidations types.EpochGroupValidations) {
	// use (epochIndex, participant) as composite key for deterministic ordering
	pk := collections.Join(epochGroupValidations.EpochIndex, epochGroupValidations.Participant)
	if err := k.EpochGroupValidationsMap.Set(ctx, pk, epochGroupValidations); err != nil {
		panic(err)
	}
}

// GetEpochGroupValidations returns a epochGroupValidations from its index
func (k Keeper) GetEpochGroupValidations(
	ctx context.Context,
	participant string,
	epochIndex uint64,

) (val types.EpochGroupValidations, found bool) {
	pk := collections.Join(epochIndex, participant)
	v, err := k.EpochGroupValidationsMap.Get(ctx, pk)
	if err != nil {
		return val, false
	}
	return v, true
}

// RemoveEpochGroupValidations removes a epochGroupValidations from the store
func (k Keeper) RemoveEpochGroupValidations(
	ctx context.Context,
	participant string,
	pocStartBlockHeight uint64,

) {
	pk := collections.Join(pocStartBlockHeight, participant)
	_ = k.EpochGroupValidationsMap.Remove(ctx, pk)
}

// GetAllEpochGroupValidations returns all epochGroupValidations
func (k Keeper) GetAllEpochGroupValidations(ctx context.Context) (list []types.EpochGroupValidations) {
	iter, err := k.EpochGroupValidationsMap.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	vals, err := iter.Values()
	if err != nil {
		return nil
	}
	return vals
}
