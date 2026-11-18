package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
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
		{"touches generation end", 2036, 1, 1800, true},
		{"starts after generation", 2037, 1, 1800, false},
		{"starts during validation", 2100, 100, 1800, false},
		{"touches validation end", 2280, 1, 1800, false},
		{"evaluation block only", 2281, 1, 1800, false},
		{"booked in trigger block", 2035, 1, 2000, false},
		{"booked after trigger", 2036, 1, 2001, false},
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
		{ReservationId: 2, Participant: participant, StartHeight: 2005, DurationBlocks: 100, CreatedAtBlockHeight: 1800, Status: types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED},
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

func TestConfirmationMaintenance_GenerationDurationChanges(t *testing.T) {
	for _, tc := range []struct {
		name               string
		start              int64
		generationDuration int64
		snapshotted        bool
	}{
		{"shortened generation no longer overlaps", 2036, 34, true},
		{"extension does not add a reservation after the trigger", 2037, 36, false},
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
			if tc.snapshotted {
				require.Equal(t, []uint64{1}, event.MaintenanceReservationIds)
			} else {
				require.Empty(t, event.MaintenanceReservationIds)
			}
			params.EpochParams.PocStageDuration = tc.generationDuration
			require.NoError(t, k.SetParams(ctx, params))
			r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED
			require.NoError(t, k.SetMaintenanceReservation(ctx, r))
			addresses, err := k.MaintenanceAddressesForConfirmation(ctx, &event)
			require.NoError(t, err)
			require.Empty(t, addresses)
		})
	}
}

func TestSnapshotMaintenanceForConfirmation_RejectsIncompleteIndex(t *testing.T) {
	k, _, ctx := setupMaintenanceTest(t)
	require.NoError(t, k.MaintenanceScheduledIndex.Set(ctx, 42))
	_, err := k.SnapshotMaintenanceForConfirmation(ctx, &types.ConfirmationPoCEvent{TriggerHeight: 2000, GenerationStartHeight: 2002})
	require.ErrorContains(t, err, "indexed maintenance reservation 42")
}

func TestCancelMaintenance_ConfirmationDeadline(t *testing.T) {
	const start = int64(500)
	for _, tc := range []struct {
		name       string
		height     int64
		cutoff     int64
		enabled    bool
		wantClosed bool
	}{
		{"before deadline", start - 37, 2, true, false},
		{"at deadline", start - 36, 2, true, true},
		{"after deadline", start - 1, 2, true, true},
		{"minimum grace period", start - 35, 0, true, true},
		{"no scheduled confirmations", start - 1, 2, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, ms, ctx := setupMaintenanceTest(t)
			participant := sample.AccAddress()
			registerParticipant(t, k, ctx, participant)
			grantCredit(t, k, ctx, participant, 100)
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			params.EpochParams.InferenceValidationCutoff = tc.cutoff
			params.EpochParams.PocStageDuration = 35
			if tc.enabled {
				params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch = 4
			}
			require.NoError(t, k.SetParams(ctx, params))
			booked, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
				Creator: participant, Participant: participant, StartHeight: start, DurationBlocks: 1,
			})
			require.NoError(t, err)
			_, err = ms.CancelMaintenance(ctx.WithBlockHeight(tc.height), &types.MsgCancelMaintenance{
				Creator: participant, ReservationId: booked.ReservationId,
			})
			addr := sdk.MustAccAddressFromBech32(participant)
			state, found := k.GetMaintenanceState(ctx, addr)
			require.True(t, found)
			if tc.wantClosed {
				require.ErrorIs(t, err, types.ErrMaintenanceCancellationClosed)
				require.Equal(t, uint64(99), state.CreditBlocks)
				require.Equal(t, booked.ReservationId, state.ScheduledReservationId)
				require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(start)))
				require.True(t, k.IsParticipantInActiveMaintenance(ctx, addr))
			} else {
				require.NoError(t, err)
				require.Equal(t, uint64(100), state.CreditBlocks)
				require.Zero(t, state.ScheduledReservationId)
			}
		})
	}
}
