package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// SetPartialUpgrade set a specific partialUpgrade in the store from its index
func (k Keeper) SetPartialUpgrade(ctx context.Context, partialUpgrade types.PartialUpgrade) {
	// key is the height
	_ = k.PartialUpgrades.Set(ctx, partialUpgrade.Height, partialUpgrade)
}

// GetPartialUpgrade returns a partialUpgrade from its index
func (k Keeper) GetPartialUpgrade(
	ctx context.Context,
	height uint64,

) (val types.PartialUpgrade, found bool) {
	v, err := k.PartialUpgrades.Get(ctx, height)
	if err != nil {
		return val, false
	}
	return v, true
}

// RemovePartialUpgrade removes a partialUpgrade from the store
func (k Keeper) RemovePartialUpgrade(
	ctx context.Context,
	height uint64,

) {
	_ = k.PartialUpgrades.Remove(ctx, height)
}

// GetAllPartialUpgrade returns all partialUpgrade
func (k Keeper) GetAllPartialUpgrade(ctx context.Context) (list []types.PartialUpgrade) {
	iter, err := k.PartialUpgrades.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	defer iter.Close()
	values, err := iter.Values()
	if err != nil {
		return nil
	}
	return values
}
