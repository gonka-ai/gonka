package keeper

import (
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// GetAllEpochBLSData returns all epoch BLS data
func (k Keeper) GetAllEpochBLSData(ctx sdk.Context) []types.EpochBLSData {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	blsDataStore := prefix.NewStore(store, types.EpochBLSDataPrefix)

	iterator := blsDataStore.Iterator(nil, nil)
	defer iterator.Close()

	var list []types.EpochBLSData
	for ; iterator.Valid(); iterator.Next() {
		var val types.EpochBLSData
		//nolint:forbidigo // Genesis code
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return list
}

// SetAllEpochBLSData sets all epoch BLS data
func (k Keeper) SetAllEpochBLSData(ctx sdk.Context, list []types.EpochBLSData) {
	for _, val := range list {
		if err := k.SetEpochBLSData(ctx, val); err != nil {
			//nolint:forbidigo // Genesis code
			panic(fmt.Sprintf("failed to set epoch bls data for epoch %d from genesis: %v", val.EpochId, err))
		}
	}
}

// GetAllThresholdSigningRequests returns all threshold signing requests
func (k Keeper) GetAllThresholdSigningRequests(ctx sdk.Context) []types.ThresholdSigningRequest {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	signingStore := prefix.NewStore(store, types.ThresholdSigningRequestPrefix)

	iterator := signingStore.Iterator(nil, nil)
	defer iterator.Close()

	var list []types.ThresholdSigningRequest
	for ; iterator.Valid(); iterator.Next() {
		var val types.ThresholdSigningRequest
		//nolint:forbidigo // Genesis code
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return list
}

// SetAllThresholdSigningRequests sets all threshold signing requests and rebuilds their expiration indices
func (k Keeper) SetAllThresholdSigningRequests(ctx sdk.Context, list []types.ThresholdSigningRequest) {
	kvStore := k.storeService.OpenKVStore(ctx)
	for _, val := range list {
		key := types.ThresholdSigningRequestKey(val.RequestId)
		//nolint:forbidigo // Genesis code
		valBytes := k.cdc.MustMarshal(&val)
		if err := kvStore.Set(key, valBytes); err != nil {
			//nolint:forbidigo // Genesis code
			panic(fmt.Sprintf("failed to set signing request %x from genesis: %v", val.RequestId, err))
		}

		// Rebuild expiration index if it is still active
		if val.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_PENDING_SIGNING ||
			val.Status == types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES {
			expirationKey := types.ExpirationIndexKey(val.DeadlineBlockHeight, val.RequestId)
			if err := kvStore.Set(expirationKey, []byte{}); err != nil {
				//nolint:forbidigo // Genesis code
				panic(fmt.Sprintf("failed to set expiration index for signing request %x: %v", val.RequestId, err))
			}
		}
	}
}

// GetAllGroupKeyValidationStates returns all group key validation states
func (k Keeper) GetAllGroupKeyValidationStates(ctx sdk.Context) []types.GroupKeyValidationState {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	validationStore := prefix.NewStore(store, types.GroupValidationPrefix)

	iterator := validationStore.Iterator(nil, nil)
	defer iterator.Close()

	var list []types.GroupKeyValidationState
	for ; iterator.Valid(); iterator.Next() {
		var val types.GroupKeyValidationState
		//nolint:forbidigo // Genesis code
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		list = append(list, val)
	}

	return list
}

// SetAllGroupKeyValidationStates sets all group key validation states
func (k Keeper) SetAllGroupKeyValidationStates(ctx sdk.Context, list []types.GroupKeyValidationState) {
	store := k.storeService.OpenKVStore(ctx)
	for _, val := range list {
		validationStateKey := types.GroupValidationKey(val.NewEpochId)
		//nolint:forbidigo // Genesis code
		bz := k.cdc.MustMarshal(&val)
		if err := store.Set(validationStateKey, bz); err != nil {
			//nolint:forbidigo // Genesis code
			panic(fmt.Sprintf("failed to set group key validation for epoch %d: %v", val.NewEpochId, err))
		}
	}
}
