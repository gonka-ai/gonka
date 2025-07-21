package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetInference sets a specific inference in the store from its index and creates/updates its stats
func (k Keeper) SetInference(ctx context.Context, inference types.Inference) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))
	b := k.cdc.MustMarshal(&inference)
	store.Set(types.InferenceKey(
		inference.Index,
	), b)

	// Create and store the stats version
	stats := types.CreateInferenceStatsStorage(inference)
	statsStore := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceStatsStorageKeyPrefix))
	statsBytes := k.cdc.MustMarshal(&stats)
	statsStore.Set(types.InferenceStatsStorageKey(
		inference.Index,
	), statsBytes)

	err := k.SetDeveloperStats(ctx, inference)
	if err != nil {
		k.LogError("error setting developer stat", types.Stat, "err", err)
	} else {
		k.LogInfo("updated developer stat", types.Stat, "inference_id", inference.InferenceId, "inference_status", inference.Status.String(), "developer", inference.RequestedBy)
	}
}

func (k Keeper) SetInferenceWithoutDevStatComputation(ctx context.Context, inference types.Inference) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))
	b := k.cdc.MustMarshal(&inference)
	store.Set(types.InferenceKey(
		inference.Index,
	), b)

	// Create and store the stats version
	stats := types.CreateInferenceStatsStorage(inference)
	statsStore := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceStatsStorageKeyPrefix))
	statsBytes := k.cdc.MustMarshal(&stats)
	statsStore.Set(types.InferenceStatsStorageKey(
		inference.Index,
	), statsBytes)
}

// GetInference returns an inference from its index, falling back to stats if full inference is not found
// The disableFallback parameter can be used to disable the fallback to stats (used in tests)
func (k Keeper) GetInference(
	ctx context.Context,
	index string,
	disableFallback ...bool,
) (val types.Inference, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceKeyPrefix))

	b := store.Get(types.InferenceKey(
		index,
	))
	if b != nil {
		k.cdc.MustUnmarshal(b, &val)
		return val, true
	}

	// Check if fallback is disabled (used in tests)
	if len(disableFallback) > 0 && disableFallback[0] {
		return val, false
	}

	// If full inference not found, try to get stats version
	statsStore := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceStatsStorageKeyPrefix))
	statsBytes := statsStore.Get(types.InferenceStatsStorageKey(
		index,
	))
	if statsBytes == nil {
		return val, false
	}

	// Convert stats to inference
	var stats types.InferenceStatsStorage
	k.cdc.MustUnmarshal(statsBytes, &stats)
	val = types.InferenceFromStatsStorage(stats)
	return val, true
}

// RemoveInference removes a inference from the store with an option to convert to stats-only before removal
func (k Keeper) RemoveInference(
	ctx context.Context,
	index string,
	convertToStats ...bool,
) {
	shouldConvertToStats := false
	if len(convertToStats) > 0 {
		shouldConvertToStats = convertToStats[0]
	}

	if shouldConvertToStats {
		// Get the inference directly from the store (not using GetInference to avoid recursion)
		inference, found := k.GetInferenceDirectFromStore(ctx, index)
		if found {
			// Create and store the stats version
			stats := types.CreateInferenceStatsStorage(inference)
			storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
			statsStore := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceStatsStorageKeyPrefix))
			statsBytes := k.cdc.MustMarshal(&stats)
			statsStore.Set(types.InferenceStatsStorageKey(
				inference.Index,
			), statsBytes)

			k.LogInfo("Converted inference to stats-only", types.Inferences,
				"inference_id", inference.InferenceId,
				"status", inference.Status.String())
		}
	}

	// Remove the full inference
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
