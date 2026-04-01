package keeper

import (
	"context"
	"fmt"

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

	mp := k.GetMaintenanceParams(ctx)
	if mp == nil || !mp.MaintenanceEnabled {
		return nil
	}

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

		// Delete consumed transition row
		if err := k.DeleteMaintenanceTransition(ctx, blockHeight, t.reservationID); err != nil {
			k.LogError("Failed to delete maintenance transition",
				types.Maintenance, "reservation_id", t.reservationID, "error", err)
		}
	}

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

	// Activation-time advisory re-check (Task 3.4 will add full logic)
	// For now, just emit a warning if caps would be exceeded
	warning := k.checkActivationTimeConcurrency(ctx, r, mp)
	if warning != "" {
		r.ActivationWarning = warning
		k.LogWarn("Maintenance reservation activated with concurrency advisory warning",
			types.Maintenance, "reservation_id", reservationID, "warning", warning)
	}

	// Transition to Active
	r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE
	if err := k.SetMaintenanceReservation(ctx, r); err != nil {
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

	// Clear participant's active reservation reference
	participantAddr, err := sdk.AccAddressFromBech32(r.Participant)
	if err != nil {
		return err
	}
	state := k.GetOrCreateMaintenanceState(ctx, participantAddr)
	state.ActiveReservationId = 0
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
func (k Keeper) checkActivationTimeConcurrency(ctx context.Context, r types.MaintenanceReservation, mp *types.MaintenanceParams) string {
	// Count currently active reservations (excluding this one which is not yet active)
	// This is a simple iteration that is bounded by the concurrent cap itself.
	// Full implementation will be added in Task 3.4.
	// For now, return empty (no warning).
	return ""
}
