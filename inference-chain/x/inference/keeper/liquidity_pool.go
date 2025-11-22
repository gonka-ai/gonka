package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetLiquidityPool stores the singleton, governance-controlled liquidity pool.
func (k Keeper) SetLiquidityPool(ctx context.Context, pool types.LiquidityPool) {
	if err := k.LiquidityPoolItem.Set(ctx, pool); err != nil {
		panic(err)
	}
	k.LogDebug("Saved LiquidityPool", types.System, "address", pool.Address)
}

// GetLiquidityPool fetches the singleton, governance-controlled liquidity pool.
func (k Keeper) GetLiquidityPool(ctx context.Context) (val types.LiquidityPool, found bool) {
	pool, err := k.LiquidityPoolItem.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return val, false
		}
		panic(err)
	}
	return pool, true
}

// RemoveLiquidityPool deletes the singleton liquidity pool from the store.
func (k Keeper) RemoveLiquidityPool(ctx context.Context) {
	if err := k.LiquidityPoolItem.Remove(ctx); err != nil && !errors.Is(err, collections.ErrNotFound) {
		panic(err)
	}
	k.LogDebug("Removed LiquidityPool", types.System)
}

// LiquidityPoolExists returns true if the singleton liquidity pool is present.
func (k Keeper) LiquidityPoolExists(ctx context.Context) bool {
	_, err := k.LiquidityPoolItem.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return false
		}
		panic(err)
	}
	return true
}
