package keeper_test

import (
	"math"
	"testing"

	cosmossdk_math "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// createTestGroupMembers builds mock group members for filterOutMaintenanceParticipants tests.
func createTestGroupMembers(addresses ...string) []*group.GroupMember {
	members := make([]*group.GroupMember, len(addresses))
	for i, addr := range addresses {
		members[i] = &group.GroupMember{
			Member: &group.Member{
				Address: addr,
				Weight:  "1",
			},
		}
	}
	return members
}

// --- Test helpers ---

// setupMaintenanceTest creates a keeper, msg server, and context with maintenance enabled.
// The mock AccountKeeper.HasAccount is configured to always return true.
func setupMaintenanceTest(t *testing.T) (keeper.Keeper, types.MsgServer, sdk.Context) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)

	// Allow AccountPermission checks to pass
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	// Concurrency checks call getParticipantPower (via GetValidator) and
	// getTotalConsensusPower (via GetLastTotalPower). By returning
	// ErrNoValidatorFound every participant resolves to zero power, and
	// total power is also zero. This means the power-based concurrency cap
	// (MaintenanceMaxConcurrentPowerBps) is effectively skipped (totalPower
	// is not positive), so only the count-based cap applies. Tests that need
	// to exercise the power cap should override these mocks.
	mocks.StakingKeeper.EXPECT().GetValidator(gomock.Any(), gomock.Any()).
		Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound).AnyTimes()
	mocks.StakingKeeper.EXPECT().GetLastTotalPower(gomock.Any()).
		Return(cosmossdk_math.ZeroInt(), nil).AnyTimes()

	// Set block height so we have room for scheduling
	ctx = ctx.WithBlockHeight(100)

	// Enable maintenance in params
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.MaintenanceParams = &types.MaintenanceParams{
		MaintenanceEnabled:                            true,
		MaintenanceMinScheduleLeadBlocks:              50,
		MaintenanceMaxWindowBlocks:                    200,
		MaintenanceMaxConcurrentValidators:            3,
		MaintenanceMaxConcurrentPowerBps:              1000,
		MaintenanceCreditCapBlocks:                    400,
		MaintenanceCreditEarnPerSuccessfulEpochBlocks: 20,
	}
	require.NoError(t, k.SetParams(ctx, params))

	return k, ms, ctx
}

// registerParticipant registers a participant with the given address in the keeper.
func registerParticipant(t *testing.T, k keeper.Keeper, ctx sdk.Context, address string) {
	t.Helper()
	participant := types.Participant{
		Index:   address,
		Address: address,
		Status:  types.ParticipantStatus_ACTIVE,
	}
	addr, err := sdk.AccAddressFromBech32(address)
	require.NoError(t, err)
	require.NoError(t, k.Participants.Set(ctx, addr, participant))
}

// grantCredit grants maintenance credit to a participant.
func grantCredit(t *testing.T, k keeper.Keeper, ctx sdk.Context, address string, blocks uint64) {
	t.Helper()
	addr, err := sdk.AccAddressFromBech32(address)
	require.NoError(t, err)
	state := k.GetOrCreateMaintenanceState(ctx, addr)
	state.CreditBlocks = blocks
	require.NoError(t, k.SetMaintenanceState(ctx, state))
}

// --- 7.1: Scheduling Tests ---

func TestScheduleMaintenance_Success(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.True(t, resp.ReservationId > 0)

	// Verify reservation was created
	r, found := k.GetMaintenanceReservation(ctx, resp.ReservationId)
	require.True(t, found)
	require.Equal(t, participant, r.Participant)
	require.Equal(t, int64(500), r.StartHeight)
	require.Equal(t, uint64(50), r.DurationBlocks)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED, r.Status)

	// Verify credit was deducted
	addr, _ := sdk.AccAddressFromBech32(participant)
	state, found := k.GetMaintenanceState(ctx, addr)
	require.True(t, found)
	require.Equal(t, uint64(50), state.CreditBlocks) // 100 - 50
	require.Equal(t, resp.ReservationId, state.ScheduledReservationId)
}

func TestScheduleMaintenance_Failures(t *testing.T) {
	t.Parallel()

	participant := sample.AccAddress()
	unknownAddr := sample.AccAddress()

	tests := []struct {
		name        string
		setup       func(t *testing.T, k keeper.Keeper, ctx sdk.Context)
		msg         *types.MsgScheduleMaintenance
		expectedErr error
	}{
		{
			name: "disabled",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				registerParticipant(t, k, ctx, participant)
				grantCredit(t, k, ctx, participant, 100)
				params, _ := k.GetParams(ctx)
				params.MaintenanceParams.MaintenanceEnabled = false
				require.NoError(t, k.SetParams(ctx, params))
			},
			msg: &types.MsgScheduleMaintenance{
				Creator: participant, Participant: participant,
				StartHeight: 500, DurationBlocks: 50,
			},
			expectedErr: types.ErrMaintenanceDisabled,
		},
		{
			name: "insufficient credit",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				registerParticipant(t, k, ctx, participant)
				grantCredit(t, k, ctx, participant, 10)
			},
			msg: &types.MsgScheduleMaintenance{
				Creator: participant, Participant: participant,
				StartHeight: 500, DurationBlocks: 50,
			},
			expectedErr: types.ErrMaintenanceInsufficientCredit,
		},
		{
			name: "insufficient lead time",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				registerParticipant(t, k, ctx, participant)
				grantCredit(t, k, ctx, participant, 100)
			},
			msg: &types.MsgScheduleMaintenance{
				Creator: participant, Participant: participant,
				StartHeight: 140, DurationBlocks: 50, // block=100, lead=50, must be >= 150
			},
			expectedErr: types.ErrMaintenanceInsufficientLeadTime,
		},
		{
			name: "duration exceeded",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				registerParticipant(t, k, ctx, participant)
				grantCredit(t, k, ctx, participant, 400)
			},
			msg: &types.MsgScheduleMaintenance{
				Creator: participant, Participant: participant,
				StartHeight: 500, DurationBlocks: 300, // max 200
			},
			expectedErr: types.ErrMaintenanceDurationExceeded,
		},
		{
			name: "already scheduled",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				registerParticipant(t, k, ctx, participant)
				grantCredit(t, k, ctx, participant, 200)
				ms := keeper.NewMsgServerImpl(k)
				_, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
					Creator: participant, Participant: participant,
					StartHeight: 500, DurationBlocks: 50,
				})
				require.NoError(t, err)
			},
			msg: &types.MsgScheduleMaintenance{
				Creator: participant, Participant: participant,
				StartHeight: 700, DurationBlocks: 50,
			},
			expectedErr: types.ErrMaintenanceAlreadyScheduled,
		},
		{
			name: "participant not found",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				// no participant registration
			},
			msg: &types.MsgScheduleMaintenance{
				Creator: unknownAddr, Participant: unknownAddr,
				StartHeight: 500, DurationBlocks: 50,
			},
			expectedErr: types.ErrParticipantNotFound,
		},
	}

	for _, tc := range tests {
		tc := tc // capture loop variable for parallel subtests
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k, ms, ctx := setupMaintenanceTest(t)
			tc.setup(t, k, ctx)
			_, err := ms.ScheduleMaintenance(ctx, tc.msg)
			require.ErrorIs(t, err, tc.expectedErr)
		})
	}
}

func TestScheduleMaintenance_RejectsCompletionHeightOverflowWithoutStateMutation(t *testing.T) {
	t.Parallel()

	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.MaintenanceParams.MaintenanceMaxWindowBlocks = 100
	require.NoError(t, k.SetParams(ctx, params))

	_, err = ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    math.MaxInt64 - 49,
		DurationBlocks: 50,
	})
	require.ErrorIs(t, err, types.ErrMaintenanceCompletionHeightOverflow)

	addr, err := sdk.AccAddressFromBech32(participant)
	require.NoError(t, err)
	state, found := k.GetMaintenanceState(ctx, addr)
	require.True(t, found)
	require.Equal(t, uint64(100), state.CreditBlocks)
	require.Zero(t, state.ScheduledReservationId)

	_, found = k.GetMaintenanceReservation(ctx, 1)
	require.False(t, found)
}

// --- 7.1: Cancellation Tests ---

func TestCancelMaintenance_Success(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	// Schedule first
	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	// Cancel
	_, err = ms.CancelMaintenance(ctx, &types.MsgCancelMaintenance{
		Creator:       participant,
		ReservationId: resp.ReservationId,
	})
	require.NoError(t, err)

	// Verify reservation is canceled
	r, found := k.GetMaintenanceReservation(ctx, resp.ReservationId)
	require.True(t, found)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_CANCELED, r.Status)

	// Verify credit was restored
	addr, _ := sdk.AccAddressFromBech32(participant)
	state, found := k.GetMaintenanceState(ctx, addr)
	require.True(t, found)
	require.Equal(t, uint64(100), state.CreditBlocks) // fully restored
	require.Equal(t, uint64(0), state.ScheduledReservationId)
}

func TestCancelMaintenance_NotFound(t *testing.T) {
	t.Parallel()
	_, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()

	_, err := ms.CancelMaintenance(ctx, &types.MsgCancelMaintenance{
		Creator:       participant,
		ReservationId: 999,
	})
	require.ErrorIs(t, err, types.ErrMaintenanceReservationNotFound)
}

func TestCancelMaintenance_NotScheduled(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	// Schedule and then activate (set status to active manually)
	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	// Manually set to active (simulating BeginBlock activation)
	r, _ := k.GetMaintenanceReservation(ctx, resp.ReservationId)
	r.Status = types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE
	require.NoError(t, k.SetMaintenanceReservation(ctx, r))

	// Cancel should fail — already active
	_, err = ms.CancelMaintenance(ctx, &types.MsgCancelMaintenance{
		Creator:       participant,
		ReservationId: resp.ReservationId,
	})
	require.ErrorIs(t, err, types.ErrMaintenanceNotScheduled)
}

func TestCancelMaintenance_CreditCapRespected(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 400) // max cap

	// Schedule to deduct
	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	// Manually set credit near cap before cancel
	addr, _ := sdk.AccAddressFromBech32(participant)
	state, _ := k.GetMaintenanceState(ctx, addr)
	state.CreditBlocks = 390 // 390 + 50 = 440, but cap is 400
	require.NoError(t, k.SetMaintenanceState(ctx, state))

	// Cancel — credit should be capped
	_, err = ms.CancelMaintenance(ctx, &types.MsgCancelMaintenance{
		Creator:       participant,
		ReservationId: resp.ReservationId,
	})
	require.NoError(t, err)

	state, _ = k.GetMaintenanceState(ctx, addr)
	require.Equal(t, uint64(400), state.CreditBlocks) // capped at 400, not 440
}

// --- 7.1: Credit Accrual Tests ---

func TestCreditAccrual_CapEnforced(t *testing.T) {
	t.Parallel()
	k, _, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	addr, _ := sdk.AccAddressFromBech32(participant)

	// Pre-load credit close to (but under) the 400-block cap so a single
	// successful-epoch grant of 20 blocks would push us past it.
	grantCredit(t, k, ctx, participant, 390)

	require.NoError(t, k.GrantMaintenanceCredit(ctx, participant, 1))

	state, found := k.GetMaintenanceState(ctx, addr)
	require.True(t, found)
	require.Equal(t, uint64(400), state.CreditBlocks, "GrantMaintenanceCredit must clamp at MaintenanceCreditCapBlocks")

	// A second grant must not push above the cap.
	require.NoError(t, k.GrantMaintenanceCredit(ctx, participant, 2))
	state, _ = k.GetMaintenanceState(ctx, addr)
	require.Equal(t, uint64(400), state.CreditBlocks, "subsequent grants must not exceed the cap")
}

// TestCreditAccrual_SuppressedAcrossEpochRange verifies that credit is not
// granted for any epoch within [LastMaintenanceEpoch, LastMaintenanceEndEpoch],
// covering multi-epoch maintenance windows after they have completed.
func TestCreditAccrual_SuppressedAcrossEpochRange(t *testing.T) {
	t.Parallel()
	k, _, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	addr, _ := sdk.AccAddressFromBech32(participant)

	// Simulate a completed multi-epoch maintenance window covering epochs 5..7.
	state := k.GetOrCreateMaintenanceState(ctx, addr)
	state.CreditBlocks = 100
	state.LastMaintenanceEpoch = 5
	state.LastMaintenanceEndEpoch = 7
	require.NoError(t, k.SetMaintenanceState(ctx, state))

	// Claims for any covered epoch must not increase credit.
	for _, e := range []uint64{5, 6, 7} {
		require.NoError(t, k.GrantMaintenanceCredit(ctx, participant, e))
		st, _ := k.GetMaintenanceState(ctx, addr)
		require.Equal(t, uint64(100), st.CreditBlocks, "credit must be suppressed for covered epoch %d", e)
	}

	// A claim for an uncovered later epoch must accrue credit normally.
	require.NoError(t, k.GrantMaintenanceCredit(ctx, participant, 8))
	st, _ := k.GetMaintenanceState(ctx, addr)
	require.Equal(t, uint64(120), st.CreditBlocks, "credit must accrue for epoch outside covered range")
}

// --- 7.1: Lifecycle Tests ---

func TestLifecycle_ActivateAndComplete(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	// Process at start height (activation)
	activateCtx := ctx.WithBlockHeight(500)
	require.NoError(t, k.ProcessMaintenanceTransitions(activateCtx))

	r, found := k.GetMaintenanceReservation(activateCtx, resp.ReservationId)
	require.True(t, found)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_ACTIVE, r.Status)

	// Verify participant is in active maintenance
	addr, _ := sdk.AccAddressFromBech32(participant)
	require.True(t, k.IsParticipantInActiveMaintenance(activateCtx, addr))

	state, _ := k.GetMaintenanceState(activateCtx, addr)
	require.Equal(t, resp.ReservationId, state.ActiveReservationId)
	require.Equal(t, uint64(0), state.ScheduledReservationId)

	// Process at end height (completion).
	// Window covers [500, 549] inclusive (50 blocks), so the COMPLETE transition
	// fires at 550 (the block AFTER the last covered block). This guarantees
	// the active duration is exactly DurationBlocks blocks.
	lastActiveCtx := ctx.WithBlockHeight(549)
	require.NoError(t, k.ProcessMaintenanceTransitions(lastActiveCtx))
	require.True(t, k.IsParticipantInActiveMaintenance(lastActiveCtx, addr),
		"participant must still be active on the final covered block")

	completeCtx := ctx.WithBlockHeight(550) // 500 + 50
	require.NoError(t, k.ProcessMaintenanceTransitions(completeCtx))

	r, found = k.GetMaintenanceReservation(completeCtx, resp.ReservationId)
	require.True(t, found)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED, r.Status)

	// Verify participant is no longer in active maintenance
	require.False(t, k.IsParticipantInActiveMaintenance(completeCtx, addr))

	state, _ = k.GetMaintenanceState(completeCtx, addr)
	require.Equal(t, uint64(0), state.ActiveReservationId)
}

func TestLifecycle_NoTransitionsAtWrongHeight(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	// Process at height 300 — nothing should happen
	earlyCtx := ctx.WithBlockHeight(300)
	require.NoError(t, k.ProcessMaintenanceTransitions(earlyCtx))

	r, found := k.GetMaintenanceReservation(earlyCtx, resp.ReservationId)
	require.True(t, found)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED, r.Status)
}

// --- 7.1: Scheduling Availability Query ---

func TestSchedulability_Success(t *testing.T) {
	t.Parallel()
	k, _, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	resp, err := k.MaintenanceSchedulability(ctx, &types.QueryMaintenanceSchedulabilityRequest{
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)
	require.True(t, resp.Schedulable)
	require.Empty(t, resp.RejectionReason)
}

func TestSchedulability_Failures(t *testing.T) {
	t.Parallel()

	participant := sample.AccAddress()

	tests := []struct {
		name           string
		setup          func(t *testing.T, k keeper.Keeper, ctx sdk.Context)
		req            *types.QueryMaintenanceSchedulabilityRequest
		reasonContains string
	}{
		{
			name: "insufficient credit",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				registerParticipant(t, k, ctx, participant)
				grantCredit(t, k, ctx, participant, 10)
			},
			req: &types.QueryMaintenanceSchedulabilityRequest{
				Participant: participant, StartHeight: 500, DurationBlocks: 50,
			},
			reasonContains: "insufficient",
		},
		{
			name: "disabled",
			setup: func(t *testing.T, k keeper.Keeper, ctx sdk.Context) {
				params, _ := k.GetParams(ctx)
				params.MaintenanceParams.MaintenanceEnabled = false
				require.NoError(t, k.SetParams(ctx, params))
			},
			req: &types.QueryMaintenanceSchedulabilityRequest{
				Participant: participant, StartHeight: 500, DurationBlocks: 50,
			},
			reasonContains: "disabled",
		},
	}

	for _, tc := range tests {
		tc := tc // capture loop variable for parallel subtests
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k, _, ctx := setupMaintenanceTest(t)
			tc.setup(t, k, ctx)
			resp, err := k.MaintenanceSchedulability(ctx, tc.req)
			require.NoError(t, err)
			require.False(t, resp.Schedulable)
			require.Contains(t, resp.RejectionReason, tc.reasonContains)
		})
	}
}

// --- 7.1: Query Tests ---

func TestQueryMaintenanceCredit(t *testing.T) {
	t.Parallel()
	k, _, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 75)

	resp, err := k.MaintenanceCredit(ctx, &types.QueryMaintenanceCreditRequest{Participant: participant})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.Equal(t, uint64(75), resp.CreditBlocks)
}

func TestQueryMaintenanceCredit_NotFound(t *testing.T) {
	t.Parallel()
	k, _, ctx := setupMaintenanceTest(t)

	resp, err := k.MaintenanceCredit(ctx, &types.QueryMaintenanceCreditRequest{
		Participant: sample.AccAddress(),
	})
	require.NoError(t, err)
	require.False(t, resp.Found)
	require.Equal(t, uint64(0), resp.CreditBlocks)
}

func TestQueryMaintenanceStatus(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	// Schedule a reservation
	schedResp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	// Query status
	resp, err := k.MaintenanceStatus(ctx, &types.QueryMaintenanceStatusRequest{Participant: participant})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.NotNil(t, resp.State)
	require.Equal(t, uint64(50), resp.State.CreditBlocks)
	require.NotNil(t, resp.ScheduledReservation)
	require.Equal(t, schedResp.ReservationId, resp.ScheduledReservation.ReservationId)
	require.Nil(t, resp.ActiveReservation)
}

func TestQueryMaintenanceScheduled(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	schedResp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	resp, err := k.MaintenanceScheduled(ctx, &types.QueryMaintenanceScheduledRequest{Participant: participant})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.NotNil(t, resp.Reservation)
	require.Equal(t, schedResp.ReservationId, resp.Reservation.ReservationId)
	require.Equal(t, int64(500), resp.Reservation.StartHeight)
}

func TestQueryMaintenanceActive(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	_, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	// No active yet
	resp, err := k.MaintenanceActive(ctx, &types.QueryMaintenanceActiveRequest{})
	require.NoError(t, err)
	require.Empty(t, resp.Reservations)

	// Activate
	activateCtx := ctx.WithBlockHeight(500)
	require.NoError(t, k.ProcessMaintenanceTransitions(activateCtx))

	resp, err = k.MaintenanceActive(activateCtx, &types.QueryMaintenanceActiveRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Reservations, 1)
	require.Equal(t, participant, resp.Reservations[0].Participant)
}

// --- 7.2: Duty Exemption Tests ---

func TestIsParticipantInActiveMaintenance(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)
	addr, _ := sdk.AccAddressFromBech32(participant)

	// Both AccAddress and string variants return false initially
	t.Run("not in maintenance initially", func(t *testing.T) {
		require.False(t, k.IsParticipantInActiveMaintenance(ctx, addr))
		require.False(t, k.IsParticipantAddressInActiveMaintenance(ctx, participant))
	})

	// Schedule — still not active
	_, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator: participant, Participant: participant,
		StartHeight: 500, DurationBlocks: 50,
	})
	require.NoError(t, err)

	t.Run("scheduled but not yet active", func(t *testing.T) {
		require.False(t, k.IsParticipantInActiveMaintenance(ctx, addr))
		require.False(t, k.IsParticipantAddressInActiveMaintenance(ctx, participant))
	})

	// Activate at block 500
	activateCtx := ctx.WithBlockHeight(500)
	require.NoError(t, k.ProcessMaintenanceTransitions(activateCtx))

	t.Run("active after activation", func(t *testing.T) {
		require.True(t, k.IsParticipantInActiveMaintenance(activateCtx, addr))
		require.True(t, k.IsParticipantAddressInActiveMaintenance(activateCtx, participant))
	})

	t.Run("invalid address returns false", func(t *testing.T) {
		require.False(t, k.IsParticipantAddressInActiveMaintenance(activateCtx, "invalid-address"))
	})

	// Window covers [500, 549]; COMPLETE fires at 550
	completeCtx := ctx.WithBlockHeight(550)
	require.NoError(t, k.ProcessMaintenanceTransitions(completeCtx))

	t.Run("not active after completion", func(t *testing.T) {
		require.False(t, k.IsParticipantInActiveMaintenance(completeCtx, addr))
		require.False(t, k.IsParticipantAddressInActiveMaintenance(completeCtx, participant))
	})
}

func TestFilterOutMaintenanceParticipants(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant1 := sample.AccAddress()
	participant2 := sample.AccAddress()
	registerParticipant(t, k, ctx, participant1)
	registerParticipant(t, k, ctx, participant2)
	grantCredit(t, k, ctx, participant1, 100)

	// Schedule and activate maintenance for participant1
	_, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant1,
		Participant:    participant1,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	activateCtx := ctx.WithBlockHeight(500)
	require.NoError(t, k.ProcessMaintenanceTransitions(activateCtx))

	// Create mock group members
	members := createTestGroupMembers(participant1, participant2)

	// Filter should remove participant1 (in maintenance)
	filtered := k.FilterOutMaintenanceParticipants(activateCtx, members)
	require.Len(t, filtered, 1)
	require.Equal(t, participant2, filtered[0].Member.Address)
}

// TestCreditAccrual_SuppressedByActiveReservation exercises the in-progress
// branch of maintenanceStateCoversEpoch: a still-ACTIVE multi-epoch window
// whose end epoch has not yet been finalised must suppress credit for any
// epoch its block range overlaps, not just the activation epoch. Pre-fix the
// claim path used a `LastMaintenanceEpoch == epoch` equality, which only
// matched the start epoch.
func TestCreditAccrual_SuppressedByActiveReservation(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)
	addr, _ := sdk.AccAddressFromBech32(participant)

	// Mid-epoch claim while the window is still ACTIVE: schedule a window
	// covering blocks [500, 599], then activate and pin epoch boundaries so
	// epoch 6's block range fully overlaps the window.
	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 100,
	})
	require.NoError(t, err)

	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 5, PocStartBlockHeight: 400}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 6, PocStartBlockHeight: 500}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 7, PocStartBlockHeight: 600}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 6))

	activateCtx := ctx.WithBlockHeight(500)
	require.NoError(t, k.ProcessMaintenanceTransitions(activateCtx))

	// Activation wrote LastMaintenanceEpoch = LastMaintenanceEndEpoch = 6 as
	// provisional values. Claiming epoch 6 while still ACTIVE must suppress
	// credit via the active-reservation branch.
	require.NoError(t, k.GrantMaintenanceCredit(activateCtx, participant, 6))
	state, _ := k.GetMaintenanceState(activateCtx, addr)
	require.Equal(t, uint64(0), state.CreditBlocks, "credit must be suppressed while window is ACTIVE")
	require.Equal(t, resp.ReservationId, state.ActiveReservationId)
}

// TestScheduleMaintenance_RejectsPoCOverlapPastFixedScanHorizon verifies that
// the phase-overlap check walks every epoch up to endHeight, not just a fixed
// 5-epoch window. The old constant maxFutureEpochsToCheck=5 silently allowed
// long windows whose tail landed in an unscanned epoch's PoC start.
func TestScheduleMaintenance_RejectsPoCOverlapPastFixedScanHorizon(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)

	// Raise the duration cap so the long window is otherwise legal.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.MaintenanceParams.MaintenanceMaxWindowBlocks = 1_000_000_000_000_000
	require.NoError(t, k.SetParams(ctx, params))

	effective := types.Epoch{Index: 1, PocStartBlockHeight: 100}
	require.NoError(t, k.SetEpoch(ctx, &effective))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, effective.Index))

	// Walk forward past where the old fixed lookahead stopped (5 future
	// epochs). Place the window's tail exactly at the first unscanned epoch's
	// PoC start — pre-fix this slips through; post-fix it must be rejected.
	lastScanned := types.NewEpochContext(effective, *params.EpochParams)
	for i := 0; i < 5; i++ {
		lastScanned = lastScanned.NextEpochContext()
	}
	firstUnscanned := lastScanned.NextEpochContext()

	startHeight := lastScanned.SetNewValidators() + 1
	durationBlocks := uint64(firstUnscanned.StartOfPoC() - startHeight + 1)
	require.Positive(t, durationBlocks)
	grantCredit(t, k, ctx, participant, durationBlocks)

	_, err = ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    startHeight,
		DurationBlocks: durationBlocks,
	})
	require.ErrorIs(t, err, types.ErrMaintenanceOverlapsPoCPhase)
}

// TestLifecycle_DisabledMidWindowCompletesNormally encodes the dev_notes
// semantics for Bug 3: flipping MaintenanceEnabled=false while a window is
// ACTIVE must not strand the COMPLETE transition. The reservation winds down
// through its natural end-of-window height and the participant exits ACTIVE
// state — preventing the "stuck-active → permanent slashing immunity" bug.
func TestLifecycle_DisabledMidWindowCompletesNormally(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	activateCtx := ctx.WithBlockHeight(500)
	require.NoError(t, k.ProcessMaintenanceTransitions(activateCtx))

	addr, _ := sdk.AccAddressFromBech32(participant)
	require.True(t, k.IsParticipantInActiveMaintenance(activateCtx, addr),
		"participant must be ACTIVE after activation")

	// Governance disables maintenance mid-window.
	params, err := k.GetParams(activateCtx)
	require.NoError(t, err)
	params.MaintenanceParams.MaintenanceEnabled = false
	require.NoError(t, k.SetParams(activateCtx, params))

	// State-machine-only semantics: in-flight ACTIVE window keeps its
	// exemption regardless of the disable flag, until natural completion.
	midCtx := activateCtx.WithBlockHeight(540)
	require.True(t, k.IsParticipantInActiveMaintenance(midCtx, addr),
		"in-flight ACTIVE window must keep exemption after disable")

	// At the scheduled COMPLETE height the transition must still fire.
	completeCtx := activateCtx.WithBlockHeight(550)
	require.NoError(t, k.ProcessMaintenanceTransitions(completeCtx))

	r, found := k.GetMaintenanceReservation(completeCtx, resp.ReservationId)
	require.True(t, found)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED, r.Status)
	require.False(t, k.IsParticipantInActiveMaintenance(completeCtx, addr))

	state, _ := k.GetMaintenanceState(completeCtx, addr)
	require.Equal(t, uint64(0), state.ActiveReservationId)
}

// TestLifecycle_DisabledBeforeActivateCancelsAndRefunds covers the
// SCHEDULED → ACTIVATE arm of Bug 3's fix: if maintenance is disabled before
// the activation height, the reservation must not enter ACTIVE. It is
// canceled, its pre-paid credit refunded, and its future COMPLETE alarm
// removed — otherwise an orphan COMPLETE row would later try to complete a
// CANCELED reservation.
func TestLifecycle_DisabledBeforeActivateCancelsAndRefunds(t *testing.T) {
	t.Parallel()
	k, ms, ctx := setupMaintenanceTest(t)
	participant := sample.AccAddress()
	registerParticipant(t, k, ctx, participant)
	grantCredit(t, k, ctx, participant, 100)

	resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
		Creator:        participant,
		Participant:    participant,
		StartHeight:    500,
		DurationBlocks: 50,
	})
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(participant)
	state, _ := k.GetMaintenanceState(ctx, addr)
	require.Equal(t, uint64(50), state.CreditBlocks, "credit deducted at schedule")

	// Disable before the activation height.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.MaintenanceParams.MaintenanceEnabled = false
	require.NoError(t, k.SetParams(ctx, params))

	activateCtx := ctx.WithBlockHeight(500)
	require.NoError(t, k.ProcessMaintenanceTransitions(activateCtx))

	r, found := k.GetMaintenanceReservation(activateCtx, resp.ReservationId)
	require.True(t, found)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_CANCELED, r.Status,
		"ACTIVATE on disabled chain must cancel the SCHEDULED reservation")

	state, _ = k.GetMaintenanceState(activateCtx, addr)
	require.Equal(t, uint64(100), state.CreditBlocks, "credit must be refunded on cancel")
	require.Equal(t, uint64(0), state.ScheduledReservationId)
	require.Equal(t, uint64(0), state.ActiveReservationId)

	// The future COMPLETE alarm must have been removed. Run the transitions
	// at the original complete height — if the row leaked, completing a
	// CANCELED reservation would log an error and leave dangling state.
	completeCtx := activateCtx.WithBlockHeight(550)
	require.NoError(t, k.ProcessMaintenanceTransitions(completeCtx))
	r, _ = k.GetMaintenanceReservation(completeCtx, resp.ReservationId)
	require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_CANCELED, r.Status,
		"status must remain CANCELED — orphan COMPLETE row would have driven it elsewhere")
}
