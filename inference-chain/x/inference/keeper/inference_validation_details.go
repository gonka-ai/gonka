package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetInferenceValidationDetails set a specific inferenceValidationDetails in the store from its index
func (k Keeper) SetInferenceValidationDetails(ctx context.Context, inferenceValidationDetails types.InferenceValidationDetails) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceValidationDetailsKeyPrefix))
	b := k.cdc.MustMarshal(&inferenceValidationDetails)
	store.Set(types.InferenceValidationDetailsKey(
		inferenceValidationDetails.EpochId,
		inferenceValidationDetails.InferenceId,
	), b)
}

// GetInferenceValidationDetails returns a inferenceValidationDetails from its index
func (k Keeper) GetInferenceValidationDetails(
	ctx context.Context,
	epochId uint64,
	inferenceId string,
) (val types.InferenceValidationDetails, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceValidationDetailsKeyPrefix))

	b := store.Get(types.InferenceValidationDetailsKey(
		epochId,
		inferenceId,
	))
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

// RemoveInferenceValidationDetails removes a inferenceValidationDetails from the store
func (k Keeper) RemoveInferenceValidationDetails(
	ctx context.Context,
	epochId uint64,
	inferenceId string,
) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceValidationDetailsKeyPrefix))
	store.Delete(types.InferenceValidationDetailsKey(
		epochId,
		inferenceId,
	))
}

func (k Keeper) GetInferenceValidationDetailsForEpoch(ctx context.Context, epochId uint64) (list []types.InferenceValidationDetails) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceValidationDetailsKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, types.InferenceValidationDetailsEpochKey(epochId))

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.InferenceValidationDetails
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}

// GetAllInferenceValidationDetails returns all inferenceValidationDetails
func (k Keeper) GetAllInferenceValidationDetails(ctx context.Context) (list []types.InferenceValidationDetails) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.InferenceValidationDetailsKeyPrefix))
	iterator := storetypes.KVStorePrefixIterator(store, []byte{})

	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.InferenceValidationDetails
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return
}
