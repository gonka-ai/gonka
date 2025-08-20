package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetTopMiner set a specific topMiner in the store from its index
func (k Keeper) SetTopMiner(ctx context.Context, topMiner types.TopMiner) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TopMinerKeyPrefix))
	b := k.cdc.MustMarshal(&topMiner)
	store.Set(types.TopMinerKey(
		topMiner.Address,
	), b)
}

// GetTopMiner returns a topMiner from its index
func (k Keeper) GetTopMiner(
	ctx context.Context,
	address string,

) (val types.TopMiner, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TopMinerKeyPrefix))

	b := store.Get(types.TopMinerKey(
		address,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveTopMiner removes a topMiner from the store
func (k Keeper) RemoveTopMiner(
	ctx context.Context,
	address string,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TopMinerKeyPrefix))
	store.Delete(types.TopMinerKey(
		address,
	))
}

// GetAllTopMiner returns all topMiner
func (k Keeper) GetAllTopMiner(ctx context.Context) (list []types.TopMiner) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TopMinerKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.TopMiner
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}
