package keeper

import (
	"context"
	"errors"
	"fmt"
	"math"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
)

// --- Reservation CRUD ---

// NextMaintenanceReservationID returns the next reservation ID and increments the counter.
func (k Keeper) NextMaintenanceReservationID(ctx context.Context) (uint64, error) {
	counter, err := k.MaintenanceReservationCounter.Get(ctx)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return 0, fmt.Errorf("failed to get maintenance reservation counter: %w", err)
		}
		counter = 0
	}
	nextID := counter + 1
	if err := k.MaintenanceReservationCounter.Set(ctx, nextID); err != nil {
		return 0, fmt.Errorf("failed to set maintenance reservation counter: %w", err)
	}
	return nextID, nil
}

// SetMaintenanceReservation stores a reservation by its ID.
func (k Keeper) SetMaintenanceReservation(ctx context.Context, r types.MaintenanceReservation) error {
	if err := k.MaintenanceReservations.Set(ctx, r.ReservationId, r); err != nil {
		return fmt.Errorf("failed to set maintenance reservation %d: %w", r.ReservationId, err)
	}
	return nil
}

// GetMaintenanceReservation retrieves a reservation by ID.
func (k Keeper) GetMaintenanceReservation(ctx context.Context, id uint64) (types.MaintenanceReservation, bool) {
	v, err := k.MaintenanceReservations.Get(ctx, id)
	if err != nil {
		return types.MaintenanceReservation{}, false
	}
	return v, true
}

// --- MaintenanceState CRUD ---

// SetMaintenanceState stores the per-participant maintenance state.
func (k Keeper) SetMaintenanceState(ctx context.Context, state types.MaintenanceState) error {
	addr, err := sdk.AccAddressFromBech32(state.Participant)
	if err != nil {
		return err
	}
	return k.MaintenanceStates.Set(ctx, addr, state)
}

// GetMaintenanceState retrieves per-participant maintenance state.
func (k Keeper) GetMaintenanceState(ctx context.Context, participant sdk.AccAddress) (types.MaintenanceState, bool) {
	v, err := k.MaintenanceStates.Get(ctx, participant)
	if err != nil {
		return types.MaintenanceState{}, false
	}
	return v, true
}

// GetOrCreateMaintenanceState retrieves or initializes maintenance state for a participant.
func (k Keeper) GetOrCreateMaintenanceState(ctx context.Context, participant sdk.AccAddress) types.MaintenanceState {
	state, found := k.GetMaintenanceState(ctx, participant)
	if !found {
		return types.MaintenanceState{
			Participant: participant.String(),
		}
	}
	return state
}

// --- Transition Schedule ---

// SetMaintenanceTransition stores a transition entry for exact block-height lookup in BeginBlock.
// transitionType: 1 = activate, 2 = complete (maps to MaintenanceTransitionType enum values).
func (k Keeper) SetMaintenanceTransition(ctx context.Context, blockHeight int64, reservationID uint64, transitionType uint32) error {
	if err := k.MaintenanceTransitions.Set(ctx, collections.Join(blockHeight, reservationID), transitionType); err != nil {
		return fmt.Errorf("failed to set maintenance transition at height %d for reservation %d: %w", blockHeight, reservationID, err)
	}
	return nil
}

// DeleteMaintenanceTransition removes a consumed transition entry.
func (k Keeper) DeleteMaintenanceTransition(ctx context.Context, blockHeight int64, reservationID uint64) error {
	return k.MaintenanceTransitions.Remove(ctx, collections.Join(blockHeight, reservationID))
}

// IterateMaintenanceTransitionsAtHeight iterates over all transitions scheduled for the exact given height.
func (k Keeper) IterateMaintenanceTransitionsAtHeight(ctx context.Context, blockHeight int64, fn func(reservationID uint64, transitionType uint32) (stop bool, err error)) error {
	rng := collections.NewPrefixedPairRange[int64, uint64](blockHeight)
	iter, err := k.MaintenanceTransitions.Iterate(ctx, rng)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return err
		}
		reservationID := kv.Key.K2()
		transitionType := kv.Value
		stop, err := fn(reservationID, transitionType)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// maxMaintenanceIterationLimit is a hard upper bound on the number of entries
// any maintenance index iteration will process. Defends against DoS via
// public queries: even if an attacker could somehow bypass the governance
// concurrency cap and create many scheduled reservations, queries will not
// burn unbounded CPU/IO. In normal operation this limit is never reached
// (active is bounded by MaintenanceMaxConcurrentValidators, scheduled is
// bounded by participant count which is small in practice).
const maxMaintenanceIterationLimit = 10000

// collectActiveAndScheduledReservations returns all reservations that are
// currently in ACTIVE or SCHEDULED state by iterating the dedicated indexes
// (MaintenanceActiveIndex + MaintenanceScheduledIndex). This avoids a full
// scan of MaintenanceStates and bounds iteration to actual maintenance
// activity rather than total participant count — important for DoS resistance
// on the public concurrency / schedulability queries.
func (k Keeper) collectActiveAndScheduledReservations(ctx context.Context) ([]types.MaintenanceReservation, error) {
	var reservations []types.MaintenanceReservation

	if err := k.iterateIndexedReservations(ctx, k.MaintenanceActiveIndex, func(r types.MaintenanceReservation) {
		reservations = append(reservations, r)
	}); err != nil {
		return nil, fmt.Errorf("failed to iterate active maintenance index: %w", err)
	}

	if err := k.iterateIndexedReservations(ctx, k.MaintenanceScheduledIndex, func(r types.MaintenanceReservation) {
		reservations = append(reservations, r)
	}); err != nil {
		return nil, fmt.Errorf("failed to iterate scheduled maintenance index: %w", err)
	}

	return reservations, nil
}

// iterateIndexedReservations walks a reservation-id index, applying fn to each
// resolved MaintenanceReservation. Returns an error if iter.Key() fails — the
// caller cannot otherwise tell that the result set is incomplete, and silently
// proceeding would corrupt downstream concurrency / exemption decisions. Stops
// after maxMaintenanceIterationLimit entries as a hard DoS safeguard. Stale
// index entries (where the underlying reservation has been deleted) are logged
// at warn level and skipped — that condition is recoverable and bounded.
func (k Keeper) iterateIndexedReservations(
	ctx context.Context,
	index collections.KeySet[uint64],
	fn func(types.MaintenanceReservation),
) error {
	iter, err := index.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	count := 0
	for ; iter.Valid(); iter.Next() {
		if count >= maxMaintenanceIterationLimit {
			k.LogWarn("Maintenance index iteration truncated at safety limit",
				types.Maintenance, "limit", maxMaintenanceIterationLimit)
			break
		}
		count++
		reservationID, err := iter.Key()
		if err != nil {
			return fmt.Errorf("failed to read maintenance index key: %w", err)
		}
		r, found := k.GetMaintenanceReservation(ctx, reservationID)
		if !found {
			// Indicates index drift (an entry pointing at a deleted reservation).
			// Log so it surfaces in monitoring; continue so a single stale entry
			// does not break callers like filterOutMaintenanceParticipants or
			// the concurrency check.
			k.LogWarn("Maintenance index references missing reservation; skipping entry",
				types.Maintenance, "reservation_id", reservationID)
			continue
		}
		fn(r)
	}
	return nil
}

// activeReservationCoversEpoch reports whether the given ACTIVE reservation's
// block range overlaps any block belonging to epochIndex. Used to suppress
// credit accrual for a granted epoch while the participant's maintenance is
// in-progress.
//
// The epoch's block range is approximated as
// [PocStartBlockHeight(epochIndex), PocStartBlockHeight(epochIndex+1) - 1].
// If the next epoch is not yet recorded (the granted epoch is the latest),
// we treat the epoch as extending indefinitely on the right — this is safe
// for credit suppression: an unbounded right edge can only widen the overlap,
// erring toward more suppression rather than missed suppression.
func (k Keeper) activeReservationCoversEpoch(ctx context.Context, r types.MaintenanceReservation, epochIndex uint64) bool {
	epoch, found := k.GetEpoch(ctx, epochIndex)
	if !found {
		// Without epoch metadata we cannot bound the overlap; fall back to a
		// permissive suppression so credit cannot leak through an unrecognized
		// epoch index.
		return true
	}
	epochStart := epoch.PocStartBlockHeight

	epochEnd := int64(math.MaxInt64)
	if next, ok := k.GetEpoch(ctx, epochIndex+1); ok && next.PocStartBlockHeight > 0 {
		epochEnd = next.PocStartBlockHeight - 1
	}

	resStart := r.StartHeight
	resEnd := r.StartHeight + int64(r.DurationBlocks) - 1

	return resStart <= epochEnd && resEnd >= epochStart
}

// maintenanceStateCoversEpoch reports whether the participant's MaintenanceState
// indicates the given epoch is covered by a maintenance window. Single source of
// truth for credit accrual (GrantMaintenanceCredit) and claim-time validation
// exemption (hasSignificantMissedValidations); both must agree, otherwise a
// multi-epoch window's mid/end epochs could be exempted from credit but not
// from missed-validation checks (or vice versa).
//
// Two branches, in order:
//
//  1. In-progress window: state.ActiveReservationId points at an ACTIVE
//     reservation whose block range overlaps the granted epoch's blocks.
//     Catches mid-window epochs where the historical [start,end] range is
//     not yet finalized (LastMaintenanceEndEpoch is provisional until COMPLETE).
//
//  2. Historical window: epochIndex falls in
//     [LastMaintenanceEpoch, LastMaintenanceEndEpoch] inclusive. The end epoch
//     is overwritten at COMPLETE in BeginBlock with the epoch in which the
//     window's last block fell, so this closed range correctly covers
//     multi-epoch windows after they finish.
//
// `epochIndex != 0` guard: proto-default LastMaintenanceEpoch is 0 for
// participants that have never activated maintenance, so without the guard
// every fresh participant would have credit suppressed at epoch 0. A
// participant who genuinely activates maintenance during epoch 0 still earns
// credit at the epoch-0 settlement; epoch 0 is the bootstrap epoch and
// short-lived, so this edge case is accepted.
//
// Tracks only the most recent window; out-of-order claims spanning multiple
// historical windows would require per-epoch tracking and are not covered.
func (k Keeper) maintenanceStateCoversEpoch(ctx context.Context, state types.MaintenanceState, epochIndex uint64) bool {
	if state.ActiveReservationId != 0 {
		if r, ok := k.GetMaintenanceReservation(ctx, state.ActiveReservationId); ok &&
			r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE &&
			k.activeReservationCoversEpoch(ctx, r, epochIndex) {
			return true
		}
	}
	return epochIndex != 0 &&
		state.LastMaintenanceEpoch <= epochIndex &&
		epochIndex <= state.LastMaintenanceEndEpoch
}

// --- Credit accrual ---

// GrantMaintenanceCredit grants maintenance credit to a participant after a
// successful reward claim. Credit is not granted if maintenance was activated
// for that participant in the claimed epoch.
//
// Idempotency: the only caller (msgServer.finishSettle) runs inside a
// cached context that is committed atomically with the claim. The claim
// flow's validateRequest rejects when no SettleAmount exists, and
// finishSettle removes the SettleAmount before invoking this function — so
// a duplicate call within the same epoch cannot reach here. If the call
// graph ever expands, add a per-epoch guard (likely a new state field).
//
// Lives on Keeper (not msgServer) so other modules and BeginBlock/EndBlock
// hooks can reuse it. Returns the bech32 decode error to the caller instead
// of swallowing it — the caller is responsible for logging.
func (k Keeper) GrantMaintenanceCredit(ctx context.Context, participant string, epochIndex uint64) error {
	mp := k.GetMaintenanceParams(ctx)
	if mp == nil || !mp.MaintenanceEnabled || mp.MaintenanceCreditEarnPerSuccessfulEpochBlocks == 0 {
		return nil
	}

	participantAddr, err := sdk.AccAddressFromBech32(participant)
	if err != nil {
		return fmt.Errorf("invalid participant address %q: %w", participant, err)
	}

	state := k.GetOrCreateMaintenanceState(ctx, participantAddr)

	// Suppress credit accrual for any epoch covered by a maintenance window.
	// Shared with hasSignificantMissedValidations so both paths give the same
	// answer to "is this epoch covered by maintenance?" — see helper docs.
	if k.maintenanceStateCoversEpoch(ctx, state, epochIndex) {
		k.LogDebug("Maintenance credit skipped: epoch covered by maintenance window",
			types.Maintenance, "participant", participant, "epoch", epochIndex,
			"active_reservation_id", state.ActiveReservationId,
			"window_start_epoch", state.LastMaintenanceEpoch,
			"window_end_epoch", state.LastMaintenanceEndEpoch)
		return nil
	}

	state.CreditBlocks += mp.MaintenanceCreditEarnPerSuccessfulEpochBlocks
	if state.CreditBlocks > mp.MaintenanceCreditCapBlocks {
		state.CreditBlocks = mp.MaintenanceCreditCapBlocks
	}

	if err := k.SetMaintenanceState(ctx, state); err != nil {
		return fmt.Errorf("failed to grant maintenance credit: %w", err)
	}
	return nil
}

// --- Convenience: check if a participant is in active maintenance ---

// IsParticipantInActiveMaintenance returns true if the participant has an active
// maintenance window.
//
// Intentionally consults only the reservation state machine, not
// MaintenanceParams.MaintenanceEnabled: an in-flight ACTIVE window keeps its
// slashing exemption until its natural COMPLETE fires, even if governance
// disables the feature mid-window. The disable flag is interpreted as
// "no new windows" (enforced at Schedule and at ACTIVATE-on-disabled via
// cancelScheduledReservationOnDisabled), not as a kill switch that revokes
// in-flight protections — that would silently punish honest validators who
// pre-committed their downtime in good faith.
func (k Keeper) IsParticipantInActiveMaintenance(ctx context.Context, participant sdk.AccAddress) bool {
	state, found := k.GetMaintenanceState(ctx, participant)
	if !found {
		return false
	}
	if state.ActiveReservationId == 0 {
		return false
	}
	r, found := k.GetMaintenanceReservation(ctx, state.ActiveReservationId)
	if !found {
		return false
	}
	return r.Status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE
}

// IsParticipantAddressInActiveMaintenance is a convenience wrapper that accepts
// a bech32 address string instead of sdk.AccAddress.
func (k Keeper) IsParticipantAddressInActiveMaintenance(ctx context.Context, address string) bool {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return false
	}
	return k.IsParticipantInActiveMaintenance(ctx, addr)
}

// filterOutMaintenanceParticipants removes group members that are currently in
// an active maintenance window. Used by GetRandomExecutor to prevent assigning
// new inference work to maintenance-covered participants.
//
// Implementation: build a single set of currently-active maintenance addresses
// (bounded by MaintenanceMaxConcurrentValidators) up-front, then O(1) lookup
// per member. This avoids one collection read per member.
func (k Keeper) filterOutMaintenanceParticipants(ctx context.Context, members []*group.GroupMember) []*group.GroupMember {
	mp := k.GetMaintenanceParams(ctx)
	if mp == nil || !mp.MaintenanceEnabled {
		return members
	}

	activeAddrs := k.CollectActiveMaintenanceAddresses(ctx)
	if len(activeAddrs) == 0 {
		return members
	}

	filtered := make([]*group.GroupMember, 0, len(members))
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		if _, inMaintenance := activeAddrs[member.Member.Address]; inMaintenance {
			k.LogDebug("Excluding maintenance-covered participant from assignment",
				types.Maintenance, "participant", member.Member.Address)
			continue
		}
		filtered = append(filtered, member)
	}
	return filtered
}

// CollectActiveMaintenanceAddresses returns the bech32 addresses of every
// participant currently in an ACTIVE maintenance window. The result size is
// bounded by MaintenanceMaxConcurrentValidators.
//
// The status check is intentionally omitted: MaintenanceActiveIndex is the
// authority for "active" and is kept in lockstep with the reservation lifecycle
// (added on activate, removed on complete/cancel). Re-checking r.Status here
// would mask index/state drift instead of surfacing it. Iterator errors are
// logged but not returned: callers (filterOutMaintenanceParticipants, CPoC,
// inference-expiry) want a fail-open default — an empty/partial set widens
// participation rather than masking maintenance, which is the safer error mode.
func (k Keeper) CollectActiveMaintenanceAddresses(ctx context.Context) map[string]struct{} {
	addrs := make(map[string]struct{})
	if err := k.iterateIndexedReservations(ctx, k.MaintenanceActiveIndex, func(r types.MaintenanceReservation) {
		addrs[r.Participant] = struct{}{}
	}); err != nil {
		k.LogError("Failed to iterate active maintenance index; result may be incomplete",
			types.Maintenance, "error", err)
	}
	return addrs
}
