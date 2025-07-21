package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// GetInferenceDirectFromStore returns an inference directly from the store without falling back to stats
// This is primarily used for testing the stats fallback functionality
func (k Keeper) GetInferenceDirectFromStore(
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

// GetInferenceStatsStorage returns an inference stats storage from its index
func (k Keeper) GetInferenceStatsStorage(
	ctx context.Context,
	index string,
) (val types.InferenceStatsStorage, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceStatsStorageKeyPrefix))

	b := store.Get(types.InferenceStatsStorageKey(
		index,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}
