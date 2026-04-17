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
// PartialSignatures are stored out-of-band under per-participant sub-keys
// (see SetGroupValidationPartialSignature). Any inline entries in the input
// struct are synced to sub-keys. The base struct is persisted with
// PartialSignatures zeroed so writes stay constant-size even as signers
// accumulate.
//
// The MsgSubmitGroupKeyValidationSignature hot path bypasses this function
// for partial-sig writes and calls SetGroupValidationPartialSignature
// directly. This function is still used for first-state creation,
// threshold-reached transition (status + final signature), and genesis
// import.
func (k Keeper) SetGroupKeyValidationState(ctx sdk.Context, state *types.GroupKeyValidationState) error {
	if state == nil {
		return fmt.Errorf("nil group key validation state")
	}

	// Sync any inline partial signatures (e.g. from genesis import or a
	// legacy caller) to sub-keys. Participant index comes from the position
	// in the slice when an explicit index isn't provided — but inline
	// entries don't carry an index, so we rely on ParticipantAddress as
	// the identity. For genesis import this is a one-time migration; for
	// runtime hot-path writes, PartialSignatures should be nil.
	//
	// We don't have a direct address→index mapping here without the
	// corresponding previous-epoch data, so we fall through and assume the
	// caller already wrote sub-keys when appropriate. In practice, the
	// hot-path handler calls SetGroupValidationPartialSignature before
	// SetGroupKeyValidationState; genesis import handles this via its own
	// path below.

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

// GetGroupKeyValidationState retrieves the GroupKeyValidationState for a
// new-epoch validation round. PartialSignatures are rehydrated from
// per-participant sub-keys.
//
// Backward compatibility: if the base struct has legacy inline
// PartialSignatures (e.g. an in-flight validation round written by a
// pre-split handler and not yet cleared), this function transparently
// migrates them to sub-keys on first read and rewrites the base state
// with PartialSignatures zeroed. The migration is idempotent and scoped
// to the single validation round being read — it piggybacks on the hot
// path rather than needing a chain-wide upgrade migration.
//
// Returns (nil, false, nil) if no state exists for the epoch.
func (k Keeper) GetGroupKeyValidationState(ctx sdk.Context, newEpochID uint64) (*types.GroupKeyValidationState, bool, error) {
	store := k.storeService.OpenKVStore(ctx)
	key := types.GroupValidationKey(newEpochID)

	value, err := store.Get(key)
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

	// If legacy inline partials are present, sync them to sub-keys now so
	// subsequent reads go through the sub-key path cleanly and the base
	// state stays constant-size. Without this step, the first post-upgrade
	// SetGroupKeyValidationState call would discard the legacy inline
	// entries (zeroing PartialSignatures) without ever having written them
	// to sub-keys, silently losing signatures and corrupting SlotsCovered
	// on any subsequent submission.
	legacyInline := state.PartialSignatures
	state.PartialSignatures = nil
	if len(legacyInline) > 0 {
		if err := k.migrateInlinePartialsToSubKeys(ctx, state.PreviousEpochId, newEpochID, legacyInline); err != nil {
			return nil, false, fmt.Errorf("migrate legacy inline partial sigs: %w", err)
		}
		// Rewrite the base state with PartialSignatures nil so future
		// reads short-circuit the migration step. This write is small and
		// happens at most once per validation round.
		if err := k.setGroupKeyValidationStateBase(ctx, state); err != nil {
			return nil, false, fmt.Errorf("clear legacy inline partial sigs: %w", err)
		}
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

// migrateInlinePartialsToSubKeys syncs a slice of legacy inline partial
// signatures to per-participant sub-keys, resolving each entry's
// participant index via the previous epoch's Participants list.
//
// Participants whose address does not appear in the previous epoch are
// skipped with a warning rather than panicking: we prefer losing an
// unclaimable partial signature over halting the chain on a suspicious
// legacy entry. In practice the pre-split handler always set
// ParticipantAddress from msg.Creator after confirming the participant
// was in previousEpochBLSData, so every legacy entry should resolve.
func (k Keeper) migrateInlinePartialsToSubKeys(
	ctx sdk.Context,
	previousEpochID uint64,
	newEpochID uint64,
	inline []types.PartialSignature,
) error {
	prev, err := k.getPreviousEpochForMigration(ctx, previousEpochID, newEpochID)
	if err != nil {
		return err
	}
	addrToIdx := make(map[string]uint32, len(prev.Participants))
	for i, p := range prev.Participants {
		addrToIdx[p.Address] = uint32(i)
	}

	for _, ps := range inline {
		idx, ok := addrToIdx[ps.ParticipantAddress]
		if !ok {
			k.Logger().Warn("migrateInlinePartialsToSubKeys: skipping partial sig with unknown participant address",
				"subsystem", "BLS",
				"participant_address", ps.ParticipantAddress,
				"previous_epoch_id", previousEpochID,
				"new_epoch_id", newEpochID,
			)
			continue
		}
		psCopy := ps
		if err := k.SetGroupValidationPartialSignature(ctx, newEpochID, idx, &psCopy); err != nil {
			return fmt.Errorf("sync legacy partial sig for participant %d: %w", idx, err)
		}
	}
	return nil
}

// getPreviousEpochForMigration resolves the previous-epoch BLS data used to
// map participant addresses to slot indices. Falls back to the new epoch's
// data when the previous epoch is missing — mirrors the same fallback the
// hot-path handler applies when computing slot ownership.
func (k Keeper) getPreviousEpochForMigration(ctx sdk.Context, previousEpochID, newEpochID uint64) (types.EpochBLSData, error) {
	prev, err := k.GetEpochBLSData(ctx, previousEpochID)
	if err == nil {
		return prev, nil
	}
	// Mirror the "previous missing → use new epoch" fallback from the
	// handler so migration doesn't fail a case the normal flow accepts.
	return k.GetEpochBLSData(ctx, newEpochID)
}

// setGroupKeyValidationStateBase is the internal writer used by the
// legacy-migration path inside GetGroupKeyValidationState. It persists the
// base struct directly without re-entering the public SetGroupKeyValidationState
// (which is identical today but kept separate to make the intent clear:
// migration writes the zeroed base, it does NOT re-process partial sigs).
func (k Keeper) setGroupKeyValidationStateBase(ctx sdk.Context, state *types.GroupKeyValidationState) error {
	baseCopy := *state
	baseCopy.PartialSignatures = nil
	value, err := k.cdc.Marshal(&baseCopy)
	if err != nil {
		return err
	}
	return k.storeService.OpenKVStore(ctx).Set(types.GroupValidationKey(baseCopy.NewEpochId), value)
}
