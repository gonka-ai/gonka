package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetInference set a specific inference in the store from its index
func (k Keeper) SetInference(ctx context.Context, inference types.Inference) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))
	b := k.cdc.MustMarshal(&inference)
	store.Set(types.InferenceKey(
		inference.Index,
	), b)
}

// GetInference returns a inference from its index
func (k Keeper) GetInference(
	ctx context.Context,
	index string,

) (val types.Inference, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))

	b := store.Get(types.InferenceKey(
		index,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveInference removes a inference from the store
func (k Keeper) RemoveInference(
	ctx context.Context,
	index string,

) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))
	store.Delete(types.InferenceKey(
		index,
	))
}

// GetAllInference returns all inference
func (k Keeper) GetAllInference(ctx context.Context) (list []types.Inference) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.Inference
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}

func (k Keeper) GetInferences(ctx context.Context, ids []string) ([]types.Inference, bool) {
	var result = make([]types.Inference, len(ids))
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))
	for i, id := range ids {
		var val types.Inference
		b := store.Get(types.InferenceKey(id))

		if b == nil {
			return nil, false
		}

		k.cdc.MustUnmarshal(b, &val)
		result[i] = val
	}

	return result, true
}
