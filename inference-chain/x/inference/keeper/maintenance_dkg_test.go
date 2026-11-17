package keeper_test

import (
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceScheduling_DKGCriticalInterval(t *testing.T) {
	// Mainnet timing: PoC validation ends at P+280, DKG starts there,
	// and the new validator set is installed at P+400.
	const epochLength = int64(15391)
	cases := []struct {
		name     string
		start    int64
		duration uint64
		wantErr  error
	}{
		{"before DKG still in PoC validation", 279, 1, types.ErrMaintenanceOverlapsPoCPhase},
		{"DKG start also ends PoC validation", 280, 1, types.ErrMaintenanceOverlapsPoCPhase},
		{"first dealing block", 281, 1, types.ErrMaintenanceOverlapsDKGPhase},
		{"verification", 286, 1, types.ErrMaintenanceOverlapsDKGPhase},
		{"dispute", 289, 1, types.ErrMaintenanceOverlapsDKGPhase},
		{"gap before validator switch", 399, 1, types.ErrMaintenanceOverlapsDKGPhase},
		{"reported unprotected interval", 281, 119, types.ErrMaintenanceOverlapsDKGPhase},
		{"ends at validator switch", 399, 2, types.ErrMaintenanceOverlapsDKGPhase},
		{"validator switch", 400, 1, types.ErrMaintenanceOverlapsDKGPhase},
		{"starts at switch and ends after", 400, 2, types.ErrMaintenanceOverlapsDKGPhase},
		{"first safe block", 401, 1, nil},
		{"full safe window", 401, 200, nil},
		{"ends before next epoch", epochLength - 2, 2, nil},
		{"crosses into next epoch", epochLength - 1, 2, types.ErrMaintenanceOverlapsPoCPhase},
	}

	// Exercise the effective epoch, its successor, and fast-forwarding beyond
	// the old five-epoch scan horizon. Each case compares the public query to
	// the transaction against the same state.
	for _, epochsAhead := range []int64{0, 1, 6} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("epoch+%d/%s", epochsAhead, tc.name), func(t *testing.T) {
				k, ms, ctx := setupMaintenanceTest(t)
				participant := sample.AccAddress()
				registerParticipant(t, k, ctx, participant)
				grantCredit(t, k, ctx, participant, 400)

				params, err := k.GetParams(ctx)
				require.NoError(t, err)
				params.EpochParams.EpochLength = epochLength
				params.EpochParams.PocStageDuration = 35
				params.EpochParams.PocExchangeDuration = 0
				params.EpochParams.PocValidationDelay = 5
				params.EpochParams.PocValidationDuration = 240
				params.EpochParams.SetNewValidatorsDelay = 120
				params.MaintenanceParams.MaintenanceMinScheduleLeadBlocks = 100
				require.NoError(t, k.SetParams(ctx, params))
				effective := types.Epoch{Index: 1, PocStartBlockHeight: 1000}
				require.NoError(t, k.SetEpoch(ctx, &effective))
				require.NoError(t, k.SetEffectiveEpochIndex(ctx, effective.Index))

				start := effective.PocStartBlockHeight + epochsAhead*epochLength + tc.start
				query, err := k.MaintenanceSchedulability(ctx, &types.QueryMaintenanceSchedulabilityRequest{
					Participant: participant, StartHeight: start, DurationBlocks: tc.duration,
				})
				require.NoError(t, err)
				require.Equal(t, tc.wantErr == nil, query.Schedulable)
				if tc.wantErr != nil {
					require.Equal(t, tc.wantErr.Error(), query.RejectionReason)
				}

				resp, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
					Creator: participant, Participant: participant, StartHeight: start, DurationBlocks: tc.duration,
				})
				addr, addrErr := sdk.AccAddressFromBech32(participant)
				require.NoError(t, addrErr)
				state, found := k.GetMaintenanceState(ctx, addr)
				require.True(t, found)
				if tc.wantErr != nil {
					require.ErrorIs(t, err, tc.wantErr)
					require.Nil(t, resp)
					require.Equal(t, uint64(400), state.CreditBlocks)
					require.Zero(t, state.ScheduledReservationId)
					_, found := k.GetMaintenanceReservation(ctx, 1)
					require.False(t, found)
				} else {
					require.NoError(t, err)
					require.Equal(t, uint64(400)-tc.duration, state.CreditBlocks)
					require.Equal(t, resp.ReservationId, state.ScheduledReservationId)
				}
			})
		}
	}
}
