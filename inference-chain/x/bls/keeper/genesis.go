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

// GetAllGroupKeyValidationStates returns all group key validation states,
// with PartialSignatures rehydrated from per-participant sub-keys so the
// exported genesis is complete.
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
		//nolint:forbidigo // Genesis code
		partials, err := k.ListGroupValidationPartialSignatures(ctx, val.NewEpochId)
		if err != nil {
			panic(fmt.Sprintf("failed to list partial sigs for epoch %d: %v", val.NewEpochId, err))
		}
		val.PartialSignatures = append(val.PartialSignatures, partials...)
		list = append(list, val)
	}

	return list
}

// SetAllGroupKeyValidationStates sets all group key validation states,
// splitting inline PartialSignatures into per-participant sub-keys so the
// imported chain state matches the post-v0.2.12 layout.
func (k Keeper) SetAllGroupKeyValidationStates(ctx sdk.Context, list []types.GroupKeyValidationState) {
	for _, val := range list {
		// Split partials out to sub-keys before persisting the base state.
		// We don't have a participant-index mapping at genesis time, so
		// derive it from the address order established by the epoch's
		// Participants list (if available).
		if len(val.PartialSignatures) > 0 {
			addrToIdx := map[string]uint32{}
			//nolint:forbidigo // Genesis code
			epochData, err := k.GetEpochBLSData(ctx, val.PreviousEpochId)
			if err == nil {
				for i, p := range epochData.Participants {
					addrToIdx[p.Address] = uint32(i)
				}
			}
			for _, ps := range val.PartialSignatures {
				idx, ok := addrToIdx[ps.ParticipantAddress]
				if !ok {
					// Fall back to a hash-of-address index collision-free
					// within this epoch: use a running counter scoped to
					// unknown addresses. Genesis import shouldn't normally
					// hit this path; if it does, the test setup is stale
					// and we fail loudly.
					//nolint:forbidigo // Genesis code
					panic(fmt.Sprintf("partial sig participant %s has no index in epoch %d participants", ps.ParticipantAddress, val.PreviousEpochId))
				}
				psCopy := ps
				//nolint:forbidigo // Genesis code
				if err := k.SetGroupValidationPartialSignature(ctx, val.NewEpochId, idx, &psCopy); err != nil {
					panic(fmt.Sprintf("failed to set partial sig for epoch %d participant %d: %v", val.NewEpochId, idx, err))
				}
			}
		}

		// Persist the base state with PartialSignatures zeroed via
		// SetGroupKeyValidationState (it handles the zeroing internally).
		valCopy := val
		//nolint:forbidigo // Genesis code
		if err := k.SetGroupKeyValidationState(ctx, &valCopy); err != nil {
			panic(fmt.Sprintf("failed to set group key validation for epoch %d: %v", val.NewEpochId, err))
		}
	}
}
