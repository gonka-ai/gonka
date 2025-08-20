package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetPartialUpgrade set a specific partialUpgrade in the store from its index
func (k Keeper) SetPartialUpgrade(ctx context.Context, partialUpgrade types.PartialUpgrade) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PartialUpgradeKeyPrefix))
	b := k.cdc.MustMarshal(&partialUpgrade)
	store.Set(types.PartialUpgradeKey(
		partialUpgrade.Height,
	), b)
}

// GetPartialUpgrade returns a partialUpgrade from its index
func (k Keeper) GetPartialUpgrade(
	ctx context.Context,
	height uint64,

) (val types.PartialUpgrade, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PartialUpgradeKeyPrefix))

	b := store.Get(types.PartialUpgradeKey(
		height,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemovePartialUpgrade removes a partialUpgrade from the store
func (k Keeper) RemovePartialUpgrade(
	ctx context.Context,
	height uint64,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PartialUpgradeKeyPrefix))
	store.Delete(types.PartialUpgradeKey(
		height,
	))
}

// GetAllPartialUpgrade returns all partialUpgrade
func (k Keeper) GetAllPartialUpgrade(ctx context.Context) (list []types.PartialUpgrade) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PartialUpgradeKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.PartialUpgrade
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}
