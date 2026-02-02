package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetLiquidityPool stores the singleton, governance-controlled liquidity pool.
func (k Keeper) SetLiquidityPool(ctx context.Context, pool types.LiquidityPool) error {
	if err := k.LiquidityPoolItem.Set(ctx, pool); err != nil {
		return err
	}
	k.LogDebug("Saved LiquidityPool", types.System, "address", pool.Address)
	return nil
}

// GetLiquidityPool fetches the singleton, governance-controlled liquidity pool.
func (k Keeper) GetLiquidityPool(ctx context.Context) (val types.LiquidityPool, found bool) {
	pool, err := k.LiquidityPoolItem.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return val, false
		}
		k.LogError("failed to get liquidity pool", types.System, "error", err)
		return val, false
	}
	return pool, true
}

// RemoveLiquidityPool deletes the singleton liquidity pool from the store.
func (k Keeper) RemoveLiquidityPool(ctx context.Context) error {
	if err := k.LiquidityPoolItem.Remove(ctx); err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	k.LogDebug("Removed LiquidityPool", types.System)
	return nil
}

// LiquidityPoolExists returns true if the singleton liquidity pool is present.
func (k Keeper) LiquidityPoolExists(ctx context.Context) bool {
	exists, err := k.LiquidityPoolItem.Has(ctx)
	if err != nil {
		k.LogError("failed to check if liquidity pool exists", types.System, "error", err)
		return false
	}
	return exists
}
