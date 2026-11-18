package inference

import (
	"context"
	"testing"

	mathsdk "cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type maintenanceConfirmationAccounts struct{ types.AccountKeeper }

func (maintenanceConfirmationAccounts) HasAccount(context.Context, sdk.AccAddress) bool { return true }

type maintenanceConfirmationStaking struct{ types.StakingKeeper }

func (maintenanceConfirmationStaking) GetValidator(context.Context, sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
}

func (maintenanceConfirmationStaking) GetLastTotalPower(context.Context) (mathsdk.Int, error) {
	return mathsdk.ZeroInt(), nil
}

type maintenanceConfirmationUpgrades struct{}

func (maintenanceConfirmationUpgrades) GetUpgradePlan(context.Context) (upgradetypes.Plan, error) {
	return upgradetypes.Plan{}, nil
}

type maintenanceConfirmationCollateral struct {
	noopCollateralKeeper
	calls   int
	balance mathsdk.Int
}

func (c *maintenanceConfirmationCollateral) Slash(_ context.Context, _ sdk.AccAddress, fraction mathsdk.LegacyDec, _ string, required mathsdk.Int) (sdk.Coin, error) {
	c.calls++
	loss := fraction.MulInt(required).TruncateInt()
	c.balance = c.balance.Sub(loss)
	return sdk.NewCoin("ngonka", loss), nil
}

func maintenanceConfirmationApp(t *testing.T) (AppModule, keeper.Keeper, sdk.Context, *maintenanceConfirmationCollateral) {
	t.Helper()
	collateral := &maintenanceConfirmationCollateral{balance: mathsdk.NewInt(1000)}
	k, ctx, _ := newMinimalInferenceKeeperWithCollateral(t, collateral)
	k.AccountKeeper = maintenanceConfirmationAccounts{}
	k.Staking = maintenanceConfirmationStaking{}
	k.UpgradeKeeper = maintenanceConfirmationUpgrades{}
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.EpochLength = 15391
	params.EpochParams.PocStageDuration = 35
	params.EpochParams.PocExchangeDuration = 0
	params.EpochParams.PocValidationDelay = 5
	params.EpochParams.PocValidationDuration = 240
	params.EpochParams.SetNewValidatorsDelay = 120
	params.EpochParams.InferenceValidationCutoff = 2
	params.EpochParams.ConfirmationPocSafetyWindow = 500
	// Force the production trigger path without depending on a lucky hash.
	params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch = 1000000
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.5)
	// PREFILL's first confirmation is tracking-only. Use an enforced recipe so
	// both the protection and the negative controls exercise real penalties.
	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_DECODE
	params.MaintenanceParams.MaintenanceEnabled = true
	params.MaintenanceParams.MaintenanceMinScheduleLeadBlocks = 100
	params.MaintenanceParams.MaintenanceMaxWindowBlocks = 200
	params.CollateralParams.GracePeriodEndEpoch = 0
	params.CollateralParams.BaseWeightRatio = types.DecimalFromFloat(0.2)
	params.CollateralParams.CollateralPerWeightUnit = types.DecimalFromFloat(1)
	params.CollateralParams.SlashFractionDowntime = types.DecimalFromFloat(0.1)
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.PrecomputeSPRTValues(ctx))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 2, PocStartBlockHeight: 1000}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 2))
	seedChallengeParticipant(t, k, ctx)
	// The helper's active participant set uses measurement height zero.
	_, err = k.FreezePocStageRecipe(ctx, 0)
	require.NoError(t, err)
	p, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	p.CurrentEpochStats.ConfirmationPoCRatio = types.DecimalFromFloat(0.8)
	require.NoError(t, k.SetParticipant(ctx, p))
	require.NoError(t, k.SetMaintenanceState(ctx, types.MaintenanceState{Participant: testutil.Executor, CreditBlocks: 400}))
	return NewAppModule(nil, k, nil, nil, nil, nil), k, ctx, collateral
}

func triggerMaintenanceConfirmation(t *testing.T, am AppModule, k keeper.Keeper, ctx sdk.Context, height int64) types.ConfirmationPoCEvent {
	t.Helper()
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	epoch, found := k.GetEffectiveEpoch(ctx)
	require.True(t, found)
	ec := types.NewEpochContext(*epoch, *params.EpochParams)
	triggerCtx := ctx.WithBlockHeight(height)
	require.NoError(t, am.checkConfirmationPoCTrigger(triggerCtx, height, &ec, params.EpochParams, params.ConfirmationPocParams, triggerCtx))
	event, found, err := k.GetActiveConfirmationPoCEvent(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, height, event.TriggerHeight)
	require.False(t, event.GetRecipe().GetTracking())
	// No host commits are stored: this event measures the offline host as zero.
	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight: height,
		ModelVotingPowers:   []*types.ModelVotingPowers{{ModelId: "m1", VotingPowers: []*types.VotingPowerEntry{{Address: testutil.Validator, VotingPower: 100}}}},
	}))
	return *event
}

func scheduleConfirmationMaintenance(t *testing.T, k keeper.Keeper, ctx sdk.Context, created, start int64, duration uint64) uint64 {
	t.Helper()
	response, err := keeper.NewMsgServerImpl(k).ScheduleMaintenance(ctx.WithBlockHeight(created), &types.MsgScheduleMaintenance{
		Creator: testutil.Executor, Participant: testutil.Executor, StartHeight: start, DurationBlocks: duration,
	})
	require.NoError(t, err)
	r, found := k.GetMaintenanceReservation(ctx, response.ReservationId)
	require.True(t, found)
	require.Equal(t, created, r.CreatedAtBlockHeight)
	return response.ReservationId
}

// Inject a reservation to test evaluation independently of the scheduling guard
// in #1850, which rejects new reservations while a confirmation is in progress.
// Such a record must not gain an exemption even if it reaches activation.
func seedReactiveConfirmationMaintenance(t *testing.T, k keeper.Keeper, ctx sdk.Context, created, start int64, duration uint64) uint64 {
	t.Helper()
	id, err := k.NextMaintenanceReservationID(ctx)
	require.NoError(t, err)
	require.NoError(t, k.SetMaintenanceReservation(ctx, types.MaintenanceReservation{
		ReservationId: id, Participant: testutil.Executor, CreatedBy: testutil.Executor,
		StartHeight: start, DurationBlocks: duration, CreatedAtBlockHeight: created,
		Status: types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_SCHEDULED,
	}))
	require.NoError(t, k.MaintenanceScheduledIndex.Set(ctx, id))
	addr := sdk.MustAccAddressFromBech32(testutil.Executor)
	state := k.GetOrCreateMaintenanceState(ctx, addr)
	state.ScheduledReservationId = id
	state.CreditBlocks -= duration
	require.NoError(t, k.SetMaintenanceState(ctx, state))
	require.NoError(t, k.SetMaintenanceTransition(ctx, start, id, uint32(types.MaintenanceTransitionType_MAINTENANCE_TRANSITION_TYPE_ACTIVATE)))
	require.NoError(t, k.SetMaintenanceTransition(ctx, start+int64(duration), id, uint32(types.MaintenanceTransitionType_MAINTENANCE_TRANSITION_TYPE_COMPLETE)))
	return id
}

func TestConfirmationMaintenance_CompletedWindowProtectsThenDutiesResume(t *testing.T) {
	for _, tc := range []struct {
		name     string
		start    int64
		duration uint64
	}{
		{"active at trigger", 1990, 200},
		{"scheduled at trigger", 2002, 200},
		{"single block at generation end", 2036, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			am, k, ctx, collateral := maintenanceConfirmationApp(t)
			start := tc.start
			end := start + int64(tc.duration)
			id := scheduleConfirmationMaintenance(t, k, ctx, 1800, start, tc.duration)
			if start < 2000 {
				require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(start)))
			}
			event := triggerMaintenanceConfirmation(t, am, k, ctx, 2000)
			require.Equal(t, []uint64{id}, event.MaintenanceReservationIds)
			if start > 2000 {
				require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(start)))
			}
			require.Contains(t, k.CollectActiveMaintenanceAddresses(ctx), testutil.Executor)
			require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(end)))
			require.Empty(t, k.CollectActiveMaintenanceAddresses(ctx))
			r, found := k.GetMaintenanceReservation(ctx, id)
			require.True(t, found)
			require.Equal(t, types.MaintenanceReservationStatus_MAINTENANCE_RESERVATION_STATUS_COMPLETED, r.Status)
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			evaluationHeight := event.GetValidationEnd(params.EpochParams) + 1
			require.Equal(t, int64(2281), evaluationHeight)
			require.Less(t, end, evaluationHeight)
			require.NoError(t, am.updateConfirmationWeights(ctx.WithBlockHeight(evaluationHeight), &event))
			requireUnchangedConfirmationWeightAndRatio(t, k, ctx)
			p, found := k.GetParticipant(ctx, testutil.Executor)
			require.True(t, found)
			require.Equal(t, types.ParticipantStatus_ACTIVE, p.Status)
			require.Zero(t, collateral.calls)
			require.Equal(t, mathsdk.NewInt(1000), collateral.balance)

			// A completed window must not become a permanent exemption. A later
			// event outside it measures the host normally and applies its penalty.
			require.NoError(t, k.ClearActiveConfirmationPoCEvent(ctx))
			next := triggerMaintenanceConfirmation(t, am, k, ctx, 2500)
			require.Empty(t, next.MaintenanceReservationIds)
			require.NoError(t, am.updateConfirmationWeights(ctx.WithBlockHeight(2781), &next))
			p, found = k.GetParticipant(ctx, testutil.Executor)
			require.True(t, found)
			require.Equal(t, types.ParticipantStatus_INACTIVE, p.Status)
			requireRatioEqual(t, p.CurrentEpochStats.ConfirmationPoCRatio, 0, 100)
			group, found := k.GetEpochGroupData(ctx, 2, "")
			require.True(t, found)
			require.Zero(t, group.ValidationWeights[0].ConfirmationWeight)
			require.Equal(t, 1, collateral.calls)
			require.Equal(t, mathsdk.NewInt(992), collateral.balance)
		})
	}
}

func TestConfirmationMaintenance_ReactiveReservationCannotExempt(t *testing.T) {
	for _, created := range []int64{2000, 2001} {
		t.Run(map[int64]string{2000: "same trigger block", 2001: "after trigger"}[created], func(t *testing.T) {
			am, k, ctx, collateral := maintenanceConfirmationApp(t)
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			params.MaintenanceParams.MaintenanceMinScheduleLeadBlocks = 1
			require.NoError(t, k.SetParams(ctx, params))
			if created == 2000 {
				scheduleConfirmationMaintenance(t, k, ctx, created, 2035, 200)
			}
			event := triggerMaintenanceConfirmation(t, am, k, ctx, 2000)
			if created == 2001 {
				seedReactiveConfirmationMaintenance(t, k, ctx, created, 2035, 200)
			}
			require.Empty(t, event.MaintenanceReservationIds)
			require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(2035)))
			require.Contains(t, k.CollectActiveMaintenanceAddresses(ctx), testutil.Executor)
			require.NoError(t, am.updateConfirmationWeights(ctx.WithBlockHeight(2281), &event))
			p, found := k.GetParticipant(ctx, testutil.Executor)
			require.True(t, found)
			require.Equal(t, types.ParticipantStatus_INACTIVE, p.Status)
			require.Equal(t, 1, collateral.calls)
		})
	}
}

func TestConfirmationMaintenance_ShorterGenerationDoesNotReopenCancellation(t *testing.T) {
	am, k, ctx, _ := maintenanceConfirmationApp(t)
	id := scheduleConfirmationMaintenance(t, k, ctx, 1800, 2036, 1)
	event := triggerMaintenanceConfirmation(t, am, k, ctx, 2000)
	require.Equal(t, []uint64{id}, event.MaintenanceReservationIds)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.PocStageDuration = 34
	require.NoError(t, k.SetParams(ctx, params))
	_, err = keeper.NewMsgServerImpl(k).CancelMaintenance(ctx.WithBlockHeight(2000), &types.MsgCancelMaintenance{
		Creator: testutil.Executor, ReservationId: id,
	})
	require.ErrorIs(t, err, types.ErrMaintenanceCancellationClosed)
}

func TestConfirmationMaintenance_ValidationOnlyDoesNotExcuseMissingGeneration(t *testing.T) {
	am, k, ctx, collateral := maintenanceConfirmationApp(t)
	scheduleConfirmationMaintenance(t, k, ctx, 1800, 2280, 1)
	event := triggerMaintenanceConfirmation(t, am, k, ctx, 2000)
	require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(2280)))
	require.NoError(t, k.ProcessMaintenanceTransitions(ctx.WithBlockHeight(2281)))
	require.NoError(t, am.updateConfirmationWeights(ctx.WithBlockHeight(2281), &event))
	p, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, types.ParticipantStatus_INACTIVE, p.Status)
	require.Equal(t, 1, collateral.calls)
}
