package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetInferenceTimeout set a specific inferenceTimeout in the store from its index
func (k Keeper) SetInferenceTimeout(ctx context.Context, inferenceTimeout types.InferenceTimeout) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceTimeoutKeyPrefix))
	b := k.cdc.MustMarshal(&inferenceTimeout)
	store.Set(types.InferenceTimeoutKey(
		inferenceTimeout.ExpirationHeight,
		inferenceTimeout.InferenceId,
	), b)
}

// GetInferenceTimeout returns a inferenceTimeout from its index
func (k Keeper) GetInferenceTimeout(
	ctx context.Context,
	expirationHeight uint64,
	inferenceId string,

) (val types.InferenceTimeout, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceTimeoutKeyPrefix))

	b := store.Get(types.InferenceTimeoutKey(
		expirationHeight,
		inferenceId,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveInferenceTimeout removes a inferenceTimeout from the store
func (k Keeper) RemoveInferenceTimeout(
	ctx context.Context,
	expirationHeight uint64,
	inferenceId string,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceTimeoutKeyPrefix))
	store.Delete(types.InferenceTimeoutKey(
		expirationHeight,
		inferenceId,
	))
}

// GetAllInferenceTimeout returns all inferenceTimeout
func (k Keeper) GetAllInferenceTimeout(ctx context.Context) (list []types.InferenceTimeout) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceTimeoutKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.InferenceTimeout
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}

// GetAllInferenceTimeoutForHeight returns all inferenceTimeouts for a given expirationHeight
func (k Keeper) GetAllInferenceTimeoutForHeight(ctx context.Context, expirationHeight uint64) (list []types.InferenceTimeout) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceTimeoutKeyPrefix))

	// Use the expirationHeight as the prefix for the iterator
	iterator := storetypes.KVStorePrefixIterator(store, types.InferenceTimeoutHeightKey(expirationHeight))

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.InferenceTimeout
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}
