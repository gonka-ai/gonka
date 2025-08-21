package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetLiquidityPool stores the singleton, governance-controlled liquidity pool.
func (k Keeper) SetLiquidityPool(ctx context.Context, pool types.LiquidityPool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	b := k.cdc.MustMarshal(&pool)
	store.Set([]byte(types.LiquidityPoolKey), b)
	k.LogDebug("Saved LiquidityPool", types.System, "address", pool.Address)
}

// GetLiquidityPool fetches the singleton, governance-controlled liquidity pool.
func (k Keeper) GetLiquidityPool(ctx context.Context) (val types.LiquidityPool, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	b := store.Get([]byte(types.LiquidityPoolKey))
	if b == nil {
		return val, false
	}
	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveLiquidityPool deletes the singleton liquidity pool from the store.
func (k Keeper) RemoveLiquidityPool(ctx context.Context) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	store.Delete([]byte(types.LiquidityPoolKey))
	k.LogDebug("Removed LiquidityPool", types.System)
}

// LiquidityPoolExists returns true if the singleton liquidity pool is present.
func (k Keeper) LiquidityPoolExists(ctx context.Context) bool {
	_, found := k.GetLiquidityPool(ctx)
	return found
}
