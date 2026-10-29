package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPruneConfirmationPoC_RemovesOnlyTheCompletedTrigger(t *testing.T) {
	k, ctx := auditKeeper(t)
	setConfirmationCleanupBudget(t, k, ctx, 100)
	plantTriggerRows(t, k, ctx, 1000, 1, true)
	plantTriggerRows(t, k, ctx, 1050, 1, true)
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, completedConfirmationEvent(8, 0, 1050)))

	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))

	requireTriggerRows(t, k, ctx, 1000, true)
	requireTriggerRows(t, k, ctx, 1050, false)
	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))
	requireTriggerRows(t, k, ctx, 1000, true)
	requireTriggerRows(t, k, ctx, 1050, false)
}

func TestPruneConfirmationPoC_ProcessesEventsInSequence(t *testing.T) {
	k, ctx := auditKeeper(t)
	setConfirmationCleanupBudget(t, k, ctx, 2)
	plantTriggerRows(t, k, ctx, 1050, 2, false)
	plantTriggerRows(t, k, ctx, 1060, 1, false)
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, completedConfirmationEvent(8, 0, 1050)))
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, completedConfirmationEvent(8, 1, 1060)))

	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))

	require.Equal(t, 0, validationCount(t, k, ctx, 1050))
	require.Equal(t, 1, validationCount(t, k, ctx, 1060))

	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))
	require.Equal(t, 0, validationCount(t, k, ctx, 1060))
}

func TestPruneConfirmationPoC_LeavesActiveAndYoungEvents(t *testing.T) {
	k, ctx := auditKeeper(t)
	setConfirmationCleanupBudget(t, k, ctx, 100)
	plantTriggerRows(t, k, ctx, 1050, 1, true)
	plantTriggerRows(t, k, ctx, 1060, 1, true)
	plantTriggerRows(t, k, ctx, 1070, 1, true)
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
		EpochIndex: 8, EventSequence: 0, TriggerHeight: 1050,
		Phase: types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}))
	active := completedConfirmationEvent(8, 1, 1060)
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, active))
	require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, active))
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, completedConfirmationEvent(10, 0, 1070)))

	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))

	requireTriggerRows(t, k, ctx, 1050, true)
	requireTriggerRows(t, k, ctx, 1060, true)
	requireTriggerRows(t, k, ctx, 1070, true)
}

func TestPruneConfirmationPoC_SpreadsALargePrefixAcrossBlocks(t *testing.T) {
	k, ctx := auditKeeper(t)
	setConfirmationCleanupBudget(t, k, ctx, 2)
	plantTriggerRows(t, k, ctx, 1050, 5, false)
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, completedConfirmationEvent(8, 0, 1050)))

	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))
	require.Equal(t, 3, validationCount(t, k, ctx, 1050))
	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))
	require.Equal(t, 1, validationCount(t, k, ctx, 1050))
	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))
	require.Equal(t, 0, validationCount(t, k, ctx, 1050))
	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))
	require.Equal(t, 0, validationCount(t, k, ctx, 1050))
}

func TestPruneConfirmationPoC_RemovalFailureDoesNotSkipAhead(t *testing.T) {
	k, ctx := auditKeeper(t)
	setConfirmationCleanupBudget(t, k, ctx, 100)
	plantTriggerRows(t, k, ctx, 1050, 4, true)
	plantTriggerRows(t, k, ctx, 1060, 1, true)
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, completedConfirmationEvent(8, 0, 1050)))
	require.NoError(t, k.SetConfirmationPoCEvent(ctx, completedConfirmationEvent(8, 1, 1060)))
	t.Cleanup(func() { keeper.SetConfirmationCleanupFailAfterForTest(0) })
	keeper.SetConfirmationCleanupFailAfterForTest(2)

	require.Error(t, k.PruneConfirmationPoC(ctx, 10))

	require.Equal(t, 3, validationCount(t, k, ctx, 1050))
	requireTriggerRows(t, k, ctx, 1060, true)
	keeper.SetConfirmationCleanupFailAfterForTest(0)
	require.NoError(t, k.PruneConfirmationPoC(ctx, 10))
	requireTriggerRows(t, k, ctx, 1050, false)
	requireTriggerRows(t, k, ctx, 1060, false)
}

func setConfirmationCleanupBudget(t *testing.T, k keeper.Keeper, ctx sdk.Context, max int64) {
	t.Helper()
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	if params.PocParams == nil {
		params.PocParams = types.DefaultPocParams()
	}
	params.PocParams.PocDataPruningEpochThreshold = 1
	if params.EpochParams == nil {
		params.EpochParams = types.DefaultEpochParams()
	}
	params.EpochParams.PocPruningMax = max
	require.NoError(t, k.SetParams(ctx, params))
}

func completedConfirmationEvent(epoch, sequence uint64, trigger int64) types.ConfirmationPoCEvent {
	return types.ConfirmationPoCEvent{
		EpochIndex:    epoch,
		EventSequence: sequence,
		TriggerHeight: trigger,
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}
}

func plantTriggerRows(t *testing.T, k keeper.Keeper, ctx sdk.Context, trigger int64, participants int, withSideCollections bool) {
	t.Helper()
	for i := 0; i < participants; i++ {
		participant := mkAddr(i + 1)
		require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
			ParticipantAddress:          participant,
			ValidatorParticipantAddress: testutil.Validator,
			PocStageStartBlockHeight:    trigger,
			ModelId:                     "m",
			ValidatedWeight:             1,
		}))
		if !withSideCollections {
			continue
		}
		require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
			ParticipantAddress:       participant,
			PocStageStartBlockHeight: trigger,
			Count:                    1,
			RootHash:                 make([]byte, 32),
			ModelId:                  "m",
		}))
		require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
			ParticipantAddress:       participant,
			PocStageStartBlockHeight: trigger,
			ModelId:                  "m",
			Weights:                  []*types.MLNodeWeight{{NodeId: "n", Weight: 1}},
		}))
	}
}

func requireTriggerRows(t *testing.T, k keeper.Keeper, ctx sdk.Context, trigger int64, present bool) {
	t.Helper()
	participant := mkAddr(1)
	hasValidation, err := k.HasPocValidationV2(ctx, trigger, participant, "m", testutil.Validator)
	require.NoError(t, err)
	require.Equal(t, present, hasValidation)
	hasCommit, err := k.HasPoCV2StoreCommit(ctx, trigger, participant, "m")
	require.NoError(t, err)
	require.Equal(t, present, hasCommit)
	addr := sdk.MustAccAddressFromBech32(participant)
	hasDistribution, err := k.MLNodeWeightDistributions.Has(ctx, collections.Join3(trigger, addr, "m"))
	require.NoError(t, err)
	require.Equal(t, present, hasDistribution)
}

func validationCount(t *testing.T, k keeper.Keeper, ctx sdk.Context, trigger int64) int {
	t.Helper()
	iter, err := k.PoCValidationsV2.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](trigger))
	require.NoError(t, err)
	defer iter.Close()
	keys, err := iter.Keys()
	require.NoError(t, err)
	return len(keys)
}
