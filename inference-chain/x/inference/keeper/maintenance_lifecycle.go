package keeper

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	cosmossdk_math "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// ProcessMaintenanceTransitions processes all maintenance lifecycle transitions
// scheduled for the exact current block height. Called from BeginBlock.
//
// Access pattern:
//  1. One prefix lookup for transition rows at the exact current block height
//  2. Iterate only the rows returned for that exact height
//  3. One direct reservation lookup per returned row
//  4. Apply transition (Scheduled->Active or Active->Completed)
//  5. Update the participant's MaintenanceState references
//  6. Delete consumed transition row
func (k Keeper) ProcessMaintenanceTransitions(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()

	// Always run, regardless of the MaintenanceEnabled flag. Transition rows
	// are keyed by exact block height — a flag flip mid-window would otherwise
	// strand the COMPLETE row at the scheduled height, leaving the reservation
	// in ACTIVE forever and (via state.ActiveReservationId) permanently
	// exempting the participant from slashing. The flag is interpreted as
	// "no new windows can be scheduled or activated"; in-flight windows wind
	// down through the normal state machine, and any SCHEDULED reservation
	// whose ACTIVATE fires after a disable gets canceled and refunded.
	mp := k.GetMaintenanceParams(ctx)
	maintenanceEnabled := mp != nil && mp.MaintenanceEnabled

	// Collect transitions to process (we must not modify during iteration)
	type pendingTransition struct {
		reservationID  uint64
		transitionType uint32
	}
	var transitions []pendingTransition

	err := k.IterateMaintenanceTransitionsAtHeight(ctx, blockHeight, func(reservationID uint64, transitionType uint32) (bool, error) {
		transitions = append(transitions, pendingTransition{
			reservationID:  reservationID,
			transitionType: transitionType,
		})
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("failed to iterate maintenance transitions at height %d: %w", blockHeight, err)
	}

	for _, t := range transitions {
		switch types.MaintenanceTransitionType(t.transitionType) {
		case types.MaintenanceTransitionType_MAINTENANCE_TRANSITION_TYPE_ACTIVATE:
			if !maintenanceEnabled {
				if err := k.cancelScheduledReservationOnDisabled(ctx, sdkCtx, t.reservationID); err != nil {
					k.LogError("Failed to cancel scheduled reservation after maintenance disabled",
						types.Maintenance, "reservation_id", t.reservationID, "error", err)
				}
				break
			}
			if err := k.activateMaintenanceReservation(ctx, sdkCtx, t.reservationID, mp); err != nil {
				k.LogError("Failed to activate maintenance reservation",
					types.Maintenance, "reservation_id", t.reservationID, "error", err)
			}
		case types.MaintenanceTransitionType_MAINTENANCE_TRANSITION_TYPE_COMPLETE:
			if err := k.completeMaintenanceReservation(ctx, sdkCtx, t.reservationID); err != nil {
				k.LogError("Failed to complete maintenance reservation",
					types.Maintenance, "reservation_id", t.reservationID, "error", err)
			}
		default:
			k.LogError("Unknown maintenance transition type",
				types.Maintenance, "reservation_id", t.reservationID, "type", t.transitionType)
		}

		// Always delete the transition row, even on error. Transitions are
		// keyed by exact block height; if we leave a failed row in place, it
		// will retry on every subsequent block (BeginBlock runs every height
		// matches), burning CPU and writing logs forever. The failure has
		// been surfaced in events/logs above; rely on operator monitoring
		// rather than infinite on-chain retry.
		if err := k.DeleteMaintenanceTransition(ctx, blockHeight, t.reservationID); err != nil {
			k.LogError("Failed to delete maintenance transition",
				types.Maintenance, "reservation_id", t.reservationID, "error", err)
		}
	}

	return nil
}

// cancelScheduledReservationOnDisabled handles the ACTIVATE transition for a
// SCHEDULED reservation whose activation height arrives after MaintenanceEnabled
// has been flipped to false. Treats the disable as "no new windows": the
// reservation never enters ACTIVE, its credit cost is refunded, and its future
// COMPLETE transition row is deleted so no orphan alarm fires later.
//
// The outer ProcessMaintenanceTransitions loop deletes the ACTIVATE transition
// row after this returns, so this helper only removes the COMPLETE row.
func (k Keeper) cancelScheduledReservationOnDisabled(ctx context.Context, sdkCtx sdk.Context, reservationID uint64) error {
	r, found := k.GetMaintenanceReservation(ctx, reservationID)
	if !found {
		return fmt.Errorf("reservation %d not found", reservationID)
	}
	if r.Status != types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED {
		return fmt.Errorf("reservation %d is not in scheduled state (status=%d)", reservationID, r.Status)
	}

	r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_CANCELED
	if err := k.SetMaintenanceReservation(ctx, r); err != nil {
		return err
	}
	if err := k.MaintenanceScheduledIndex.Remove(ctx, reservationID); err != nil {
		return err
	}

	participantAddr, err := sdk.AccAddressFromBech32(r.Participant)
	if err != nil {
		return err
	}
	state := k.GetOrCreateMaintenanceState(ctx, participantAddr)
	state.CreditBlocks += r.DurationBlocks
	if mp := k.GetMaintenanceParams(ctx); mp != nil && state.CreditBlocks > mp.MaintenanceCreditCapBlocks {
		state.CreditBlocks = mp.MaintenanceCreditCapBlocks
	}
	state.ScheduledReservationId = 0
	if err := k.SetMaintenanceState(ctx, state); err != nil {
		return err
	}

	// Remove the future COMPLETE alarm. Mirrors the height arithmetic from
	// msg_server_schedule_maintenance.go where it was originally written.
	if r.DurationBlocks > uint64(math.MaxInt64) || r.StartHeight > math.MaxInt64-int64(r.DurationBlocks) {
		return fmt.Errorf("reservation %d has invalid start/duration that would overflow completion height", reservationID)
	}
	completeHeight := r.StartHeight + int64(r.DurationBlocks)
	if err := k.DeleteMaintenanceTransition(ctx, completeHeight, reservationID); err != nil {
		return fmt.Errorf("failed to delete orphan complete transition: %w", err)
	}

	k.LogInfo("Maintenance window canceled because maintenance is disabled",
		types.Maintenance,
		"reservation_id", reservationID,
		"participant", r.Participant,
		"credit_refunded", r.DurationBlocks,
	)

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"maintenance_canceled",
		sdk.NewAttribute("reservation_id", fmt.Sprint(reservationID)),
		sdk.NewAttribute("participant", r.Participant),
		sdk.NewAttribute("credit_refunded", fmt.Sprint(r.DurationBlocks)),
		sdk.NewAttribute("reason", "maintenance_disabled"),
	))

	return nil
}

// activateMaintenanceReservation transitions a reservation from Scheduled to Active.
func (k Keeper) activateMaintenanceReservation(ctx context.Context, sdkCtx sdk.Context, reservationID uint64, mp *types.MaintenanceParams) error {
	r, found := k.GetMaintenanceReservation(ctx, reservationID)
	if !found {
		return fmt.Errorf("reservation %d not found", reservationID)
	}
	if r.Status != types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED {
		return fmt.Errorf("reservation %d is not in scheduled state (status=%d)", reservationID, r.Status)
	}

	// Concurrency caps were enforced when the reservation was scheduled. Re-check
	// at activation time for visibility only: chain conditions (validator set,
	// other windows, governance params) may have changed in the interim. The
	// reservation still activates regardless; any breach is recorded as an
	// advisory ActivationWarning on the reservation and surfaced in the log.
	warning := k.checkActivationTimeConcurrency(ctx, r, mp)
	if warning != "" {
		r.ActivationWarning = warning
		k.LogInfo("Maintenance reservation activated with concurrency advisory warning",
			types.Maintenance, "reservation_id", reservationID, "warning", warning)
	}

	// Transition to Active
	r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE
	if err := k.SetMaintenanceReservation(ctx, r); err != nil {
		return err
	}

	// Add to the active index for O(A) MaintenanceActive query
	if err := k.MaintenanceActiveIndex.Set(ctx, reservationID); err != nil {
		return err
	}

	// Remove from scheduled index now that the reservation is active.
	if err := k.MaintenanceScheduledIndex.Remove(ctx, reservationID); err != nil {
		return err
	}

	// Update participant's MaintenanceState
	participantAddr, err := sdk.AccAddressFromBech32(r.Participant)
	if err != nil {
		return err
	}
	state := k.GetOrCreateMaintenanceState(ctx, participantAddr)
	state.ActiveReservationId = reservationID
	state.ScheduledReservationId = 0

	// Mark the activation epoch as the start of the credit-suppression range.
	// LastMaintenanceEndEpoch is provisionally set to the same value and will be
	// overwritten at completion to reflect the true end epoch. While the window
	// is still ACTIVE, the active-reservation overlap check in GrantMaintenanceCredit
	// covers any in-progress epoch regardless of these fields.
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if found {
		state.LastMaintenanceEpoch = epochIndex
		state.LastMaintenanceEndEpoch = epochIndex
	}

	if err := k.SetMaintenanceState(ctx, state); err != nil {
		return err
	}

	k.LogInfo("Maintenance window activated",
		types.Maintenance,
		"reservation_id", reservationID,
		"participant", r.Participant,
		"start_height", r.StartHeight,
		"duration_blocks", r.DurationBlocks,
	)

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"maintenance_activated",
		sdk.NewAttribute("reservation_id", fmt.Sprint(reservationID)),
		sdk.NewAttribute("participant", r.Participant),
		sdk.NewAttribute("start_height", fmt.Sprint(r.StartHeight)),
		sdk.NewAttribute("duration_blocks", fmt.Sprint(r.DurationBlocks)),
	))

	return nil
}

// completeMaintenanceReservation transitions a reservation from Active to Completed.
func (k Keeper) completeMaintenanceReservation(ctx context.Context, sdkCtx sdk.Context, reservationID uint64) error {
	r, found := k.GetMaintenanceReservation(ctx, reservationID)
	if !found {
		return fmt.Errorf("reservation %d not found", reservationID)
	}
	if r.Status != types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE {
		return fmt.Errorf("reservation %d is not in active state (status=%d)", reservationID, r.Status)
	}

	// Transition to Completed
	r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED
	if err := k.SetMaintenanceReservation(ctx, r); err != nil {
		return err
	}

	// Remove from the active index
	if err := k.MaintenanceActiveIndex.Remove(ctx, reservationID); err != nil {
		return err
	}

	// Clear participant's active reservation reference and record the end
	// epoch so credit accrual stays suppressed for any past epoch the window
	// covered, even after the window has finished. Combined with the start
	// epoch written at activation, this defines a closed [start, end] range
	// over which GrantMaintenanceCredit will skip — preventing double-dip
	// when a multi-epoch maintenance ends and a delayed claim arrives for
	// one of the covered epochs.
	participantAddr, err := sdk.AccAddressFromBech32(r.Participant)
	if err != nil {
		return err
	}
	state := k.GetOrCreateMaintenanceState(ctx, participantAddr)
	state.ActiveReservationId = 0
	if epochIndex, found := k.GetEffectiveEpochIndex(ctx); found {
		state.LastMaintenanceEndEpoch = epochIndex
	}
	if err := k.SetMaintenanceState(ctx, state); err != nil {
		return err
	}

	k.LogInfo("Maintenance window completed",
		types.Maintenance,
		"reservation_id", reservationID,
		"participant", r.Participant,
	)

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"maintenance_completed",
		sdk.NewAttribute("reservation_id", fmt.Sprint(reservationID)),
		sdk.NewAttribute("participant", r.Participant),
	))

	return nil
}

// checkActivationTimeConcurrency re-checks concurrency caps at activation time.
// Returns a warning string if current caps would reject this reservation; empty string otherwise.
// The reservation still activates regardless — this is advisory only.
func (k Keeper) checkActivationTimeConcurrency(ctx context.Context, r types.MaintenanceReservation, mp *types.MaintenanceParams) string {
	endHeight := r.StartHeight + int64(r.DurationBlocks) - 1

	// Collect all active/scheduled reservations and check overlap
	reservations, err := k.collectActiveAndScheduledReservations(ctx)
	if err != nil {
		return ""
	}

	concurrentCount := uint32(0)
	concurrentPower := cosmossdk_math.ZeroInt()

	for _, other := range reservations {
		if other.ReservationId == r.ReservationId {
			continue // skip self
		}
		otherEnd := other.StartHeight + int64(other.DurationBlocks) - 1
		if other.StartHeight <= endHeight && otherEnd >= r.StartHeight {
			concurrentCount++
			otherAddr, addrErr := sdk.AccAddressFromBech32(other.Participant)
			if addrErr == nil {
				concurrentPower = concurrentPower.Add(k.getParticipantPower(ctx, otherAddr))
			}
		}
	}

	var warnings []string

	// Check count cap (including this reservation)
	if concurrentCount+1 > mp.MaintenanceMaxConcurrentValidators {
		warnings = append(warnings, fmt.Sprintf(
			"concurrent count %d exceeds cap %d", concurrentCount+1, mp.MaintenanceMaxConcurrentValidators))
	}

	// Check power cap (including this participant). All math is integer-only
	// (math.Int): any string persisted to state must be deterministic across
	// architectures, and the bps multiplication must not silently overflow.
	participantAddr, err := sdk.AccAddressFromBech32(r.Participant)
	if err != nil {
		return ""
	}
	participantPower := k.getParticipantPower(ctx, participantAddr)
	totalPower := k.getTotalConsensusPower(ctx)
	if totalPower.IsPositive() && mp.MaintenanceMaxConcurrentPowerBps > 0 {
		maxPower := totalPower.MulRaw(int64(mp.MaintenanceMaxConcurrentPowerBps)).QuoRaw(10000)
		used := concurrentPower.Add(participantPower)
		if used.GT(maxPower) {
			warnings = append(warnings, fmt.Sprintf(
				"concurrent power %s exceeds cap %s (cap_bps=%d total=%s)",
				used.String(), maxPower.String(),
				mp.MaintenanceMaxConcurrentPowerBps, totalPower.String()))
		}
	}

	if len(warnings) == 0 {
		return ""
	}
	// Sort to guarantee deterministic state writes across all validators.
	// concurrentPower is summed from a non-deterministic iteration order, but
	// the final sum is order-independent; the warnings slice itself is built
	// in a fixed order today, but sorting makes the determinism explicit and
	// future-proof against reordering of the checks above.
	sort.Strings(warnings)
	return "activation-time concurrency advisory: " + strings.Join(warnings, "; ")
}
