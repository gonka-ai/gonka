package keeper

import (
	"context"
	"fmt"
	"math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) ScheduleMaintenance(goCtx context.Context, msg *types.MsgScheduleMaintenance) (*types.MsgScheduleMaintenanceResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}

	sdkCtx := sdk.UnwrapSDKContext(goCtx)
	blockHeight := sdkCtx.BlockHeight()

	// Authorization: only the participant themselves may schedule their own
	// maintenance window. Without this check, anyone could drain another
	// participant's credit and force them into maintenance.
	if msg.Creator != msg.Participant {
		return nil, types.ErrInvalidPermission
	}

	// Check maintenance is enabled
	mp := k.GetMaintenanceParams(goCtx)
	if mp == nil || !mp.MaintenanceEnabled {
		return nil, types.ErrMaintenanceDisabled
	}

	// Validate participant address
	participantAddr, err := sdk.AccAddressFromBech32(msg.Participant)
	if err != nil {
		return nil, types.ErrMaintenanceInvalidParticipant
	}

	// Verify participant exists
	_, err = k.Participants.Get(goCtx, participantAddr)
	if err != nil {
		return nil, types.ErrParticipantNotFound
	}

	// Validate duration is positive and within limits
	if msg.DurationBlocks == 0 {
		return nil, types.ErrMaintenanceZeroDuration
	}
	if msg.DurationBlocks > mp.MaintenanceMaxWindowBlocks {
		return nil, types.ErrMaintenanceDurationExceeded
	}
	if msg.DurationBlocks > math.MaxInt64 || msg.StartHeight > math.MaxInt64-int64(msg.DurationBlocks) {
		return nil, types.ErrMaintenanceCompletionHeightOverflow
	}

	// Validate lead time: startHeight must be at least MinScheduleLeadBlocks
	// in the future. Using strict less-than so a request scheduled exactly
	// MinScheduleLeadBlocks blocks ahead is accepted.
	if msg.StartHeight < blockHeight+int64(mp.MaintenanceMinScheduleLeadBlocks) {
		return nil, types.ErrMaintenanceInsufficientLeadTime
	}

	// Check participant does not already have a scheduled reservation
	state := k.GetOrCreateMaintenanceState(goCtx, participantAddr)
	if state.ScheduledReservationId != 0 {
		return nil, types.ErrMaintenanceAlreadyScheduled
	}

	// Check sufficient credit
	if state.CreditBlocks < msg.DurationBlocks {
		return nil, types.ErrMaintenanceInsufficientCredit
	}

	// Check epoch-critical phase overlap (PoC and DKG/SetNewValidators)
	if err := k.checkEpochPhaseOverlap(goCtx, msg.StartHeight, msg.DurationBlocks, mp); err != nil {
		return nil, err
	}

	// Check concurrency limits
	if err := k.checkConcurrencyLimits(goCtx, msg.StartHeight, msg.DurationBlocks, participantAddr, mp); err != nil {
		return nil, err
	}

	// Check same-participant overlap with existing reservations
	if err := k.checkParticipantOverlap(goCtx, participantAddr, msg.StartHeight, msg.DurationBlocks); err != nil {
		return nil, err
	}

	// All checks pass — create the reservation
	reservationID, err := k.NextMaintenanceReservationID(goCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate reservation ID: %w", err)
	}

	reservation := types.MaintenanceReservation{
		ReservationId:  reservationID,
		Participant:    msg.Participant,
		StartHeight:    msg.StartHeight,
		DurationBlocks: msg.DurationBlocks,
		CreatedBy:      msg.Creator,
		Status:         types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED,
	}

	if err := k.SetMaintenanceReservation(goCtx, reservation); err != nil {
		return nil, err
	}

	// Add to scheduled index for bounded iteration in queries.
	if err := k.MaintenanceScheduledIndex.Set(goCtx, reservationID); err != nil {
		return nil, fmt.Errorf("failed to index scheduled reservation: %w", err)
	}

	// Deduct credit
	state.CreditBlocks -= msg.DurationBlocks
	state.ScheduledReservationId = reservationID
	if err := k.SetMaintenanceState(goCtx, state); err != nil {
		return nil, err
	}

	// Add transition schedule entries for BeginBlock lifecycle.
	// Window covers [startHeight, startHeight + durationBlocks - 1] inclusive,
	// so the COMPLETE transition must fire at (startHeight + durationBlocks),
	// the block AFTER the last covered block. Otherwise the reservation would
	// be deactivated one block early and the effective active duration would
	// be (DurationBlocks - 1).
	activateType := uint32(types.MaintenanceTransitionType_MAINTENANCE_TRANSITION_TYPE_ACTIVATE)
	completeType := uint32(types.MaintenanceTransitionType_MAINTENANCE_TRANSITION_TYPE_COMPLETE)
	completeHeight := msg.StartHeight + int64(msg.DurationBlocks)

	if err := k.SetMaintenanceTransition(goCtx, msg.StartHeight, reservationID, activateType); err != nil {
		return nil, err
	}
	if err := k.SetMaintenanceTransition(goCtx, completeHeight, reservationID, completeType); err != nil {
		return nil, err
	}

	k.LogInfo("Maintenance window scheduled",
		types.Maintenance,
		"reservation_id", reservationID,
		"participant", msg.Participant,
		"start_height", msg.StartHeight,
		"duration_blocks", msg.DurationBlocks,
	)

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		"maintenance_scheduled",
		sdk.NewAttribute("reservation_id", fmt.Sprint(reservationID)),
		sdk.NewAttribute("participant", msg.Participant),
		sdk.NewAttribute("start_height", fmt.Sprint(msg.StartHeight)),
		sdk.NewAttribute("duration_blocks", fmt.Sprint(msg.DurationBlocks)),
	))

	return &types.MsgScheduleMaintenanceResponse{ReservationId: reservationID}, nil
}
