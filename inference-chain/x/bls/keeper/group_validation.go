package keeper

import (
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// groupValidationPartialSigStore returns a prefix.Store scoped to all partial
// signatures collected for a single new-epoch validation round. Keys within
// the returned store are the sub-keys produced by
// types.GroupValidationPartialSigSubKey.
func (k Keeper) groupValidationPartialSigStore(ctx sdk.Context, newEpochID uint64) prefix.Store {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return prefix.NewStore(store, types.GroupValidationPartialSigEpochPrefix(newEpochID))
}

// SetGroupValidationPartialSignature writes a single partial signature under
// its own sub-key. Cost is constant in the number of signers that already
// submitted, so every signer in a round pays the same gas regardless of
// submission order. This is the hot path called by
// SubmitGroupKeyValidationSignature.
//
// If the same participant resubmits with additional slot coverage, merge the
// new slot indices and signature bytes into the existing entry so one
// sub-key per participant stays the invariant. The merge keeps the per-write
// cost bounded by that participant's own slot count (48 bytes per slot),
// independent of how many other signers landed.
func (k Keeper) SetGroupValidationPartialSignature(
	ctx sdk.Context,
	newEpochID uint64,
	participantIndex uint32,
	ps *types.PartialSignature,
) error {
	if ps == nil {
		return fmt.Errorf("nil partial signature")
	}
	store := k.groupValidationPartialSigStore(ctx, newEpochID)
	subKey := types.GroupValidationPartialSigSubKey(participantIndex)

	existing := store.Get(subKey)
	if existing != nil {
		var prior types.PartialSignature
		if err := k.cdc.Unmarshal(existing, &prior); err != nil {
			return fmt.Errorf("unmarshal existing partial sig: %w", err)
		}
		// Preserve the original participant address — it's the same
		// participant by index, and the on-chain address should never drift
		// within an epoch. Append slot coverage and signature bytes.
		if prior.ParticipantAddress == "" {
			prior.ParticipantAddress = ps.ParticipantAddress
		}
		prior.SlotIndices = append(prior.SlotIndices, ps.SlotIndices...)
		prior.Signature = append(prior.Signature, ps.Signature...)
		ps = &prior
	}

	value, err := k.cdc.Marshal(ps)
	if err != nil {
		return fmt.Errorf("marshal partial sig: %w", err)
	}
	store.Set(subKey, value)
	return nil
}

// GetGroupValidationPartialSignature reads the partial signature submitted by
// a specific participant for the given new-epoch validation round. Returns
// (nil, nil) if the participant has not submitted.
func (k Keeper) GetGroupValidationPartialSignature(
	ctx sdk.Context,
	newEpochID uint64,
	participantIndex uint32,
) (*types.PartialSignature, error) {
	value := k.groupValidationPartialSigStore(ctx, newEpochID).Get(types.GroupValidationPartialSigSubKey(participantIndex))
	if value == nil {
		return nil, nil
	}
	var ps types.PartialSignature
	if err := k.cdc.Unmarshal(value, &ps); err != nil {
		return nil, err
	}
	return &ps, nil
}

// ListGroupValidationPartialSignatures returns every partial signature
// collected so far for a new-epoch validation round, in ascending
// participant-index order. Used by the handler's duplicate-slot check and by
// the threshold-reached aggregation path.
func (k Keeper) ListGroupValidationPartialSignatures(
	ctx sdk.Context,
	newEpochID uint64,
) ([]types.PartialSignature, error) {
	it := k.groupValidationPartialSigStore(ctx, newEpochID).Iterator(nil, nil)
	defer it.Close()

	var out []types.PartialSignature
	for ; it.Valid(); it.Next() {
		var ps types.PartialSignature
		if err := k.cdc.Unmarshal(it.Value(), &ps); err != nil {
			return nil, fmt.Errorf("unmarshal partial sig: %w", err)
		}
		out = append(out, ps)
	}
	return out, nil
}

// DeleteGroupValidationPartialSignaturesForEpoch removes every partial
// signature sub-key for a validation round. Not called on the normal success
// path — the signatures remain as an audit trail until the epoch's state is
// explicitly cleaned up.
func (k Keeper) DeleteGroupValidationPartialSignaturesForEpoch(ctx sdk.Context, newEpochID uint64) error {
	store := k.groupValidationPartialSigStore(ctx, newEpochID)
	it := store.Iterator(nil, nil)

	var keysToDelete [][]byte
	for ; it.Valid(); it.Next() {
		keysToDelete = append(keysToDelete, append([]byte(nil), it.Key()...))
	}
	it.Close()

	for _, key := range keysToDelete {
		store.Delete(key)
	}
	return nil
}

// SetGroupKeyValidationState persists the base GroupKeyValidationState.
//
// PartialSignatures live out-of-band under per-participant sub-keys (see
// SetGroupValidationPartialSignature). The base is persisted with
// PartialSignatures zeroed so writes stay constant-size as signers
// accumulate.
//
// Inline PartialSignatures on the input are first synced to sub-keys via
// syncInlinePartialsToSubKeys, resolving addr→index from the previous
// epoch's Participants list. This covers genesis import and the
// upgrade-time legacy-state migration. The runtime hot path passes
// PartialSignatures == nil and pays no sync overhead.
func (k Keeper) SetGroupKeyValidationState(ctx sdk.Context, state *types.GroupKeyValidationState) error {
	if state == nil {
		return fmt.Errorf("nil group key validation state")
	}

	if len(state.PartialSignatures) > 0 {
		if err := k.syncInlinePartialsToSubKeys(ctx, state); err != nil {
			return fmt.Errorf("sync inline partial sigs: %w", err)
		}
	}

	baseCopy := *state
	baseCopy.PartialSignatures = nil

	store := k.storeService.OpenKVStore(ctx)
	key := types.GroupValidationKey(baseCopy.NewEpochId)
	value, err := k.cdc.Marshal(&baseCopy)
	if err != nil {
		return err
	}
	return store.Set(key, value)
}

// GetGroupKeyValidationState returns the validation state with
// PartialSignatures rehydrated from per-participant sub-keys. Pure read:
// the upgrade handler migrates any pre-split inline entries in a single
// pass, so this function never writes state.
//
// Returns (nil, false, nil) if no state exists for the epoch.
func (k Keeper) GetGroupKeyValidationState(ctx sdk.Context, newEpochID uint64) (*types.GroupKeyValidationState, bool, error) {
	value, err := k.storeService.OpenKVStore(ctx).Get(types.GroupValidationKey(newEpochID))
	if err != nil {
		return nil, false, err
	}
	if value == nil {
		return nil, false, nil
	}
	state := &types.GroupKeyValidationState{}
	if err := k.cdc.Unmarshal(value, state); err != nil {
		return nil, false, err
	}
	subKeyed, err := k.ListGroupValidationPartialSignatures(ctx, newEpochID)
	if err != nil {
		return nil, false, fmt.Errorf("list partial sigs: %w", err)
	}
	if len(subKeyed) > 0 {
		state.PartialSignatures = subKeyed
	}
	return state, true, nil
}

// syncInlinePartialsToSubKeys writes each inline PartialSignature under
// its per-participant sub-key. Participant index is resolved via the
// previous epoch's Participants list, falling back to the new epoch's
// list when the previous is missing — same fallback the hot-path handler
// uses for slot ownership. Entries with an unresolvable address are
// skipped with a warning; dropping one unclaimable legacy partial is
// preferable to halting an upgrade block.
func (k Keeper) syncInlinePartialsToSubKeys(ctx sdk.Context, state *types.GroupKeyValidationState) error {
	prev, err := k.GetEpochBLSData(ctx, state.PreviousEpochId)
	if err != nil {
		prev, err = k.GetEpochBLSData(ctx, state.NewEpochId)
		if err != nil {
			return fmt.Errorf("resolve participants for epoch %d (fallback %d): %w",
				state.PreviousEpochId, state.NewEpochId, err)
		}
	}
	addrToIdx := make(map[string]uint32, len(prev.Participants))
	for i, p := range prev.Participants {
		addrToIdx[p.Address] = uint32(i)
	}
	for _, ps := range state.PartialSignatures {
		idx, ok := addrToIdx[ps.ParticipantAddress]
		if !ok {
			k.Logger().Warn("syncInlinePartialsToSubKeys: skipping partial sig with unknown participant address",
				"subsystem", "BLS",
				"participant_address", ps.ParticipantAddress,
				"previous_epoch_id", state.PreviousEpochId,
				"new_epoch_id", state.NewEpochId,
			)
			continue
		}
		psCopy := ps
		if err := k.SetGroupValidationPartialSignature(ctx, state.NewEpochId, idx, &psCopy); err != nil {
			return fmt.Errorf("sync partial sig for participant %d: %w", idx, err)
		}
	}
	return nil
}
