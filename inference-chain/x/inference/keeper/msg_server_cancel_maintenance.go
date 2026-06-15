package keeper

import (
	"context"
	"fmt"
	"math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CancelMaintenance(goCtx context.Context, msg *types.MsgCancelMaintenance) (*types.MsgCancelMaintenanceResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}

	sdkCtx := sdk.UnwrapSDKContext(goCtx)

	// Look up the reservation
	r, found := k.GetMaintenanceReservation(goCtx, msg.ReservationId)
	if !found {
		return nil, types.ErrMaintenanceReservationNotFound
	}

	// Only scheduled reservations can be canceled
	if r.Status != types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED {
		return nil, types.ErrMaintenanceNotScheduled
	}

	// Authorization: only the participant themselves may cancel their
	// reservation. ScheduleMaintenance enforces Creator == Participant, so
	// CreatedBy on existing rows is always equal to Participant — comparing
	// against r.CreatedBy here is redundant and would silently widen
	// authorization if the schedule-side constraint were ever relaxed.
	if msg.Creator != r.Participant {
		return nil, types.ErrInvalidPermission
	}

	// Transition to canceled
	r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_CANCELED
	if err := k.SetMaintenanceReservation(goCtx, r); err != nil {
		return nil, err
	}

	// Remove from scheduled index (no-op if absent).
	if err := k.MaintenanceScheduledIndex.Remove(goCtx, r.ReservationId); err != nil {
		return nil, fmt.Errorf("failed to remove scheduled index entry: %w", err)
	}

	// Restore credit to participant
	participantAddr, err := sdk.AccAddressFromBech32(r.Participant)
	if err != nil {
		return nil, err
	}
	state := k.GetOrCreateMaintenanceState(goCtx, participantAddr)
	state.CreditBlocks += r.DurationBlocks
	// Cap credit at max
	mp := k.GetMaintenanceParams(goCtx)
	if mp != nil && state.CreditBlocks > mp.MaintenanceCreditCapBlocks {
		state.CreditBlocks = mp.MaintenanceCreditCapBlocks
	}
	state.ScheduledReservationId = 0
	if err := k.SetMaintenanceState(goCtx, state); err != nil {
		return nil, err
	}

	// Remove transition schedule entries. The COMPLETE transition height must
	// match what ScheduleMaintenance wrote: startHeight + DurationBlocks
	// (the block AFTER the last covered block). Guard against int64 overflow:
	// DurationBlocks is uint64 and although ScheduleMaintenance enforces a
	// governance-bounded cap, defense-in-depth here keeps a corrupt or
	// pre-validation reservation from producing a wrap-around height that
	// would silently miss the delete and leave a stale transition row behind.
	if r.DurationBlocks > math.MaxInt64 || r.StartHeight > math.MaxInt64-int64(r.DurationBlocks) {
		return nil, fmt.Errorf("reservation %d has invalid start/duration that would overflow completion height", r.ReservationId)
	}
	completeHeight := r.StartHeight + int64(r.DurationBlocks)
	if err := k.DeleteMaintenanceTransition(goCtx, r.StartHeight, r.ReservationId); err != nil {
		return nil, fmt.Errorf("failed to delete activate transition: %w", err)
	}
	if err := k.DeleteMaintenanceTransition(goCtx, completeHeight, r.ReservationId); err != nil {
		return nil, fmt.Errorf("failed to delete complete transition: %w", err)
	}

	k.LogInfo("Maintenance window canceled",
		types.Maintenance,
		"reservation_id", r.ReservationId,
		"participant", r.Participant,
		"credit_restored", r.DurationBlocks,
	)

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"maintenance_canceled",
		sdk.NewAttribute("reservation_id", fmt.Sprint(r.ReservationId)),
		sdk.NewAttribute("participant", r.Participant),
		sdk.NewAttribute("credit_restored", fmt.Sprint(r.DurationBlocks)),
	))

	return &types.MsgCancelMaintenanceResponse{}, nil
}
