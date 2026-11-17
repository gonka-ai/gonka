package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestSnapshotMaintenanceForConfirmation_Boundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		start    int64
		duration uint64
		created  int64
		want     bool
	}{
		{"ends before generation", 1802, 200, 1700, false},
		{"touches generation start", 1803, 200, 1700, true},
		{"starts with generation", 2002, 200, 1800, true},
		{"starts during validation", 2100, 100, 1800, true},
		{"touches validation end", 2280, 1, 1800, true},
		{"evaluation block only", 2281, 1, 1800, false},
		{"booked in trigger block", 2100, 100, 2000, false},
		{"booked after trigger", 2101, 100, 2001, false},
		{"legacy creation height unknown", 2002, 200, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, _, ctx := setupMaintenanceTest(t)
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			params.EpochParams.PocStageDuration = 35
			params.EpochParams.PocExchangeDuration = 0
			params.EpochParams.PocValidationDelay = 5
			params.EpochParams.PocValidationDuration = 240
			// Governance may lower the maximum after a reservation was accepted.
			// Its original duration must still determine event coverage.
			params.MaintenanceParams.MaintenanceMaxWindowBlocks = 1
			require.NoError(t, k.SetParams(ctx, params))
			r := types.MaintenanceReservation{
				ReservationId: 1, Participant: sample.AccAddress(),
				StartHeight: tc.start, DurationBlocks: tc.duration,
				CreatedAtBlockHeight: tc.created,
				Status:               types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED,
			}
			require.NoError(t, k.SetMaintenanceReservation(ctx, r))
			require.NoError(t, k.MaintenanceScheduledIndex.Set(ctx, r.ReservationId))
			event := types.ConfirmationPoCEvent{EpochIndex: 2, TriggerHeight: 2000, GenerationStartHeight: 2002}
			require.Equal(t, int64(2280), event.GetValidationEnd(params.EpochParams))
			event.MaintenanceReservationIds, err = k.SnapshotMaintenanceForConfirmation(ctx, &event)
			require.NoError(t, err)
			r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE
			require.NoError(t, k.SetMaintenanceReservation(ctx, r))
			addresses, err := k.MaintenanceAddressesForConfirmation(ctx, &event)
			require.NoError(t, err)
			if tc.want {
				require.Equal(t, map[string]struct{}{r.Participant: {}}, addresses)
			} else {
				require.Empty(t, addresses)
			}
		})
	}
}

func TestConfirmationMaintenanceSnapshot_PersistsAndRequiresActivation(t *testing.T) {
	k, _, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	for _, r := range []types.MaintenanceReservation{
		{ReservationId: 10, Participant: participant, StartHeight: 1900, DurationBlocks: 150, CreatedAtBlockHeight: 1700, Status: types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE},
		{ReservationId: 2, Participant: participant, StartHeight: 2050, DurationBlocks: 100, CreatedAtBlockHeight: 1800, Status: types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED},
	} {
		require.NoError(t, k.SetMaintenanceReservation(ctx, r))
	}
	require.NoError(t, k.MaintenanceActiveIndex.Set(ctx, 10))
	require.NoError(t, k.MaintenanceScheduledIndex.Set(ctx, 2))
	event := types.ConfirmationPoCEvent{EpochIndex: 2, TriggerHeight: 2000, GenerationStartHeight: 2002}
	ids, err := k.SnapshotMaintenanceForConfirmation(ctx, &event)
	require.NoError(t, err)
	event.MaintenanceReservationIds = ids
	require.Equal(t, []uint64{2, 10}, event.MaintenanceReservationIds)
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, event))
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, event))
	stored, found, err := k.GetConfirmationPoCEvent(ctx, 2, 0)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, event.MaintenanceReservationIds, stored.MaintenanceReservationIds)
	active, found, err := k.GetActiveConfirmationPoCEvent(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, event.MaintenanceReservationIds, active.MaintenanceReservationIds)

	for _, status := range []types.MaintenanceReservationStatus{
		types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE,
		types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED,
		types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_CANCELED,
		types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED,
	} {
		t.Run(status.String(), func(t *testing.T) {
			for _, id := range event.MaintenanceReservationIds {
				r, ok := k.GetMaintenanceReservation(ctx, id)
				require.True(t, ok)
				r.Status = status
				require.NoError(t, k.SetMaintenanceReservation(ctx, r))
			}
			addresses, err := k.MaintenanceAddressesForConfirmation(ctx, &stored)
			require.NoError(t, err)
			if status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE || status == types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED {
				require.Equal(t, map[string]struct{}{participant: {}}, addresses)
			} else {
				require.Empty(t, addresses)
			}
		})
	}
	// A missing snapshot row must not silently turn an intended exemption into
	// a collateral slash. Propagate the read failure before evaluating anyone.
	require.NoError(t, k.MaintenanceReservations.Remove(ctx, 10))
	_, err = k.MaintenanceAddressesForConfirmation(ctx, &stored)
	require.Error(t, err)
}

func TestConfirmationMaintenance_ValidationDurationChanges(t *testing.T) {
	for _, tc := range []struct {
		name               string
		start              int64
		validationDuration int64
		want               bool
	}{
		{"shortened event makes window evaluation-only", 2200, 159, false},
		{"extended event reaches prebooked future window", 2300, 260, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, _, ctx := setupMaintenanceTest(t)
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			params.EpochParams.PocStageDuration = 35
			params.EpochParams.PocExchangeDuration = 0
			params.EpochParams.PocValidationDelay = 5
			params.EpochParams.PocValidationDuration = 240
			require.NoError(t, k.SetParams(ctx, params))
			r := types.MaintenanceReservation{ReservationId: 1, Participant: sample.AccAddress(), StartHeight: tc.start, DurationBlocks: 1, CreatedAtBlockHeight: 1800, Status: types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED}
			require.NoError(t, k.SetMaintenanceReservation(ctx, r))
			require.NoError(t, k.MaintenanceScheduledIndex.Set(ctx, 1))
			event := types.ConfirmationPoCEvent{TriggerHeight: 2000, GenerationStartHeight: 2002}
			require.Equal(t, int64(2280), event.GetValidationEnd(params.EpochParams))
			event.MaintenanceReservationIds, err = k.SnapshotMaintenanceForConfirmation(ctx, &event)
			require.NoError(t, err)
			require.Equal(t, []uint64{1}, event.MaintenanceReservationIds)
			params.EpochParams.PocValidationDuration = tc.validationDuration
			require.NoError(t, k.SetParams(ctx, params))
			r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED
			require.NoError(t, k.SetMaintenanceReservation(ctx, r))
			addresses, err := k.MaintenanceAddressesForConfirmation(ctx, &event)
			require.NoError(t, err)
			if tc.want {
				require.Contains(t, addresses, r.Participant)
			} else {
				require.Empty(t, addresses)
			}
		})
	}
}

func TestSnapshotMaintenanceForConfirmation_RejectsIncompleteIndex(t *testing.T) {
	k, _, ctx := setupMaintenanceTest(t)
	require.NoError(t, k.MaintenanceScheduledIndex.Set(ctx, 42))
	_, err := k.SnapshotMaintenanceForConfirmation(ctx, &types.ConfirmationPoCEvent{TriggerHeight: 2000, GenerationStartHeight: 2002})
	require.ErrorContains(t, err, "indexed maintenance reservation 42")
}
