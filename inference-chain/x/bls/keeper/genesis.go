package keeper

import (
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// WalkEpochBLSData visits each stored EpochBLSData entry (base record only)
// in key order. Unlike GetEpochBLSData, it does not rehydrate DealerParts,
// VerificationSubmissions, or DealerComplaints from sub-keys — callers that
// need those merged should call GetEpochBLSData per entry.
func (k Keeper) WalkEpochBLSData(ctx sdk.Context, walkFn func(types.EpochBLSData) error) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	blsDataStore := prefix.NewStore(store, types.EpochBLSDataPrefix)

	iterator := blsDataStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.EpochBLSData
		if err := k.cdc.Unmarshal(iterator.Value(), &val); err != nil {
			return fmt.Errorf("unmarshal epoch bls data: %w", err)
		}
		if err := walkFn(val); err != nil {
			return err
		}
	}

	return nil
}

// GetAllEpochBLSData returns all epoch BLS data with DealerParts,
// VerificationSubmissions, and DealerComplaints rehydrated from their
// per-entry sub-keys. Used by genesis export so the dumped state carries
// the full layout, not just the stripped base struct.
func (k Keeper) GetAllEpochBLSData(ctx sdk.Context) []types.EpochBLSData {
	var list []types.EpochBLSData
	if err := k.WalkEpochBLSData(ctx, func(base types.EpochBLSData) error {
		full, err := k.GetEpochBLSData(ctx, base.EpochId)
		if err != nil {
			return fmt.Errorf("rehydrate epoch %d: %w", base.EpochId, err)
		}
		list = append(list, full)
		return nil
	}); err != nil {
		//nolint:forbidigo // Genesis code
		panic(fmt.Sprintf("failed to iterate epoch bls data: %v", err))
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

// WalkRawThresholdSigningRequests visits each stored ThresholdSigningRequest
// base record in key order, without rehydrating split PartialSignatures from
// sub-keys. Callers that need the full merged shape should call
// GetSigningStatus per entry or use GetAllThresholdSigningRequests.
func (k Keeper) WalkRawThresholdSigningRequests(ctx sdk.Context, walkFn func(types.ThresholdSigningRequest) error) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	signingStore := prefix.NewStore(store, types.ThresholdSigningRequestPrefix)

	iterator := signingStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.ThresholdSigningRequest
		if err := k.cdc.Unmarshal(iterator.Value(), &val); err != nil {
			return fmt.Errorf("unmarshal threshold signing request: %w", err)
		}
		if err := walkFn(val); err != nil {
			return err
		}
	}

	return nil
}

// GetAllThresholdSigningRequests returns all threshold signing requests,
// with PartialSignatures rehydrated from per-submitter sub-keys so the
// exported genesis is complete.
func (k Keeper) GetAllThresholdSigningRequests(ctx sdk.Context) []types.ThresholdSigningRequest {
	var list []types.ThresholdSigningRequest
	if err := k.WalkRawThresholdSigningRequests(ctx, func(val types.ThresholdSigningRequest) error {
		partials, err := k.ListThresholdPartialSignatures(ctx, val.RequestId)
		if err != nil {
			return fmt.Errorf("list partial sigs for request %x: %w", val.RequestId, err)
		}
		val.PartialSignatures = append(val.PartialSignatures, partials...)
		list = append(list, val)
		return nil
	}); err != nil {
		//nolint:forbidigo // Genesis code
		panic(fmt.Sprintf("failed to iterate threshold signing requests: %v", err))
	}
	return list
}

// SetAllThresholdSigningRequests sets all threshold signing requests and
// rebuilds their expiration indices. Inline PartialSignatures from the
// genesis payload are split into per-submitter sub-keys via
// storeThresholdSigningRequest's sync loop, so the imported chain state
// matches the post-v0.2.12 layout.
func (k Keeper) SetAllThresholdSigningRequests(ctx sdk.Context, list []types.ThresholdSigningRequest) {
	kvStore := k.storeService.OpenKVStore(ctx)
	for _, val := range list {
		valCopy := val
		//nolint:forbidigo // Genesis code
		if err := k.storeThresholdSigningRequest(ctx, &valCopy); err != nil {
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

// WalkGroupKeyValidationStates visits each stored GroupKeyValidationState
// base record in key order, without rehydrating split PartialSignatures
// from sub-keys. Callers that need the full merged shape should call
// GetGroupKeyValidationState per entry or use GetAllGroupKeyValidationStates.
func (k Keeper) WalkGroupKeyValidationStates(ctx sdk.Context, walkFn func(types.GroupKeyValidationState) error) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	validationStore := prefix.NewStore(store, types.GroupValidationPrefix)

	iterator := validationStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var val types.GroupKeyValidationState
		if err := k.cdc.Unmarshal(iterator.Value(), &val); err != nil {
			return fmt.Errorf("unmarshal group key validation state: %w", err)
		}
		if err := walkFn(val); err != nil {
			return err
		}
	}

	return nil
}

// GetAllGroupKeyValidationStates returns all group key validation states,
// with PartialSignatures rehydrated from per-participant sub-keys so the
// exported genesis is complete.
func (k Keeper) GetAllGroupKeyValidationStates(ctx sdk.Context) []types.GroupKeyValidationState {
	var list []types.GroupKeyValidationState
	if err := k.WalkGroupKeyValidationStates(ctx, func(val types.GroupKeyValidationState) error {
		partials, err := k.ListGroupValidationPartialSignatures(ctx, val.NewEpochId)
		if err != nil {
			return fmt.Errorf("list partial sigs for epoch %d: %w", val.NewEpochId, err)
		}
		val.PartialSignatures = append(val.PartialSignatures, partials...)
		list = append(list, val)
		return nil
	}); err != nil {
		//nolint:forbidigo // Genesis code
		panic(fmt.Sprintf("failed to iterate group key validation states: %v", err))
	}
	return list
}

// SetAllGroupKeyValidationStates sets all group key validation states.
// SetGroupKeyValidationState splits inline PartialSignatures into
// per-participant sub-keys, so the imported chain state matches the
// post-v0.2.12 layout.
func (k Keeper) SetAllGroupKeyValidationStates(ctx sdk.Context, list []types.GroupKeyValidationState) {
	for _, val := range list {
		valCopy := val
		//nolint:forbidigo // Genesis code
		if err := k.SetGroupKeyValidationState(ctx, &valCopy); err != nil {
			panic(fmt.Sprintf("failed to set group key validation for epoch %d: %v", val.NewEpochId, err))
		}
	}
}
