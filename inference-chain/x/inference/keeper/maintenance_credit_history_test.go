package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestCreditAccrual_UsesWindowBlocksAcrossEffectiveEpochs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		duration   uint64
		wantCredit uint64
	}{
		{"spans both epochs", 2, 98},
		{"ends on the last block of the old epoch", 1, 119},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, ms, ctx := setupMaintenanceTest(t)
			participant := sample.AccAddress()
			addr := sdk.MustAccAddressFromBech32(participant)
			registerParticipant(t, k, ctx, participant)
			grantCredit(t, k, ctx, participant, 100)

			// An accepted reservation still takes effect if an epoch boundary
			// moves after scheduling. Set the effective ranges after booking.
			_, err := ms.ScheduleMaintenance(ctx, &types.MsgScheduleMaintenance{
				Creator: participant, Participant: participant, StartHeight: 499, DurationBlocks: tc.duration,
			})
			require.NoError(t, err)
			k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 5, EffectiveBlockHeight: 400, LastBlockHeight: 499})
			k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 6, EffectiveBlockHeight: 500, LastBlockHeight: 599})
			require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
			require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(499)))
			require.NoError(t, k.SetEffectiveEpochIndex(ctx, 6))
			end := int64(499 + tc.duration)
			if end == 500 {
				require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(end)))
			}

			// The previous epoch can be claimed while the window is still
			// active. Both active and completed windows suppress that grant.
			require.NoError(t, k.GrantMaintenanceCredit(ctx.WithBlockHeight(500), participant, 5))
			state, found := k.GetMaintenanceState(ctx, addr)
			require.True(t, found)
			require.Equal(t, 100-tc.duration, state.CreditBlocks)
			if end > 500 {
				require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(end)))
			}

			require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))
			require.NoError(t, k.GrantMaintenanceCredit(ctx.WithBlockHeight(600), participant, 6))
			state, found = k.GetMaintenanceState(ctx, addr)
			require.True(t, found)
			require.Equal(t, tc.wantCredit, state.CreditBlocks)
		})
	}
}

func TestClaimRewards_MaintenanceCreditWithDelayedClaim(t *testing.T) {
	for _, tc := range []struct {
		name            string
		previousStart   int64
		wantCreditDelta uint64
	}{
		{"old window still suppresses credit after a later maintenance", 6000, 0},
		{"next PoC starts before the claimed epoch ends", maintClaimNextPoc + 36, 0},
		{"a window in the claim block's epoch does not suppress the previous epoch", 0, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMaintenanceClaim(t, heightsFrom(2000, 1000))
			params, err := f.k.GetParams(f.ctx)
			require.NoError(t, err)
			params.ValidationParams.ClaimValidationEnabled = false
			require.NoError(t, f.k.SetParams(f.ctx, params))

			switchHeight := f.nextPoc + 400
			group, found := f.k.GetEpochGroupData(f.ctx, maintClaimEpoch, "")
			require.True(t, found)
			group.EffectiveBlockHeight = maintClaimEpochPoc + 400
			group.LastBlockHeight = switchHeight - 1
			f.k.SetEpochGroupData(f.ctx, group)
			if tc.previousStart > 0 {
				f.runWindow(t, tc.previousStart, 1)
			}

			grantCredit(t, f.k, f.ctx, testutil.Creator, 100)
			start := switchHeight + 200
			_, err = f.ms.ScheduleMaintenance(f.ctx.WithBlockHeight(start-100), &types.MsgScheduleMaintenance{
				Creator: testutil.Creator, Participant: testutil.Creator, StartHeight: start, DurationBlocks: 1,
			})
			require.NoError(t, err)
			require.NoError(t, f.k.ProcessMaintenanceTransitions(f.ctx.WithBlockHeight(start)))
			require.NoError(t, f.k.ProcessMaintenanceTransitions(f.ctx.WithBlockHeight(start+1)))
			before, found := f.k.GetMaintenanceState(f.ctx, f.addr)
			require.True(t, found)
			require.Equal(t, maintClaimEpoch+1, before.LastMaintenanceEpoch)

			claimCtx := f.ctx.WithBlockHeight(start + 10)
			response, err := f.ms.ClaimRewards(claimCtx, &types.MsgClaimRewards{
				Creator: testutil.Creator, EpochIndex: maintClaimEpoch, Seed: int64(maintClaimClaimSeed),
			})
			require.NoError(t, err)
			require.Equal(t, "Rewards claimed successfully", response.Result)
			require.Equal(t, uint64(1500), response.Amount)
			after, found := f.k.GetMaintenanceState(f.ctx, f.addr)
			require.True(t, found)
			require.Equal(t, before.CreditBlocks+tc.wantCreditDelta, after.CreditBlocks)
		})
	}
}
