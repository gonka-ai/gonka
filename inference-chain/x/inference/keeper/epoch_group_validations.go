package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupValidations set a specific epochGroupValidations in the store from its index
func (k Keeper) SetEpochGroupValidations(ctx context.Context, epochGroupValidations types.EpochGroupValidations) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupValidationsKeyPrefix))
	b := k.cdc.MustMarshal(&epochGroupValidations)
	store.Set(types.EpochGroupValidationsKey(
		epochGroupValidations.Participant,
		epochGroupValidations.EpochIndex,
	), b)
}

// GetEpochGroupValidations returns a epochGroupValidations from its index
func (k Keeper) GetEpochGroupValidations(
	ctx context.Context,
	participant string,
	epochIndex uint64,

) (val types.EpochGroupValidations, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupValidationsKeyPrefix))

	b := store.Get(types.EpochGroupValidationsKey(
		participant,
		epochIndex,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveEpochGroupValidations removes a epochGroupValidations from the store
func (k Keeper) RemoveEpochGroupValidations(
	ctx context.Context,
	participant string,
	pocStartBlockHeight uint64,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupValidationsKeyPrefix))
	store.Delete(types.EpochGroupValidationsKey(
		participant,
		pocStartBlockHeight,
	))
}

// GetAllEpochGroupValidations returns all epochGroupValidations
func (k Keeper) GetAllEpochGroupValidations(ctx context.Context) (list []types.EpochGroupValidations) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.EpochGroupValidationsKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.EpochGroupValidations
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}
