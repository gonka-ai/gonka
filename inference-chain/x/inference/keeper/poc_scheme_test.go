package keeper_test

import (
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFreezePocStageRecipe_WriteOnce(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	params := types.DefaultParams()
	params.PocParams.PocStrongerRngEnabled = true
	require.NoError(t, k.SetParams(ctx, params))
	first, err := k.FreezePocStageRecipe(ctx, 100)
	require.NoError(t, err)
	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_DECODE
	params.PocParams.PocStrongerRngEnabled = false
	params.PocParams.Models[0].Schemes[1].DecodeSteps = 99
	require.NoError(t, k.SetParams(ctx, params))
	second, err := k.FreezePocStageRecipe(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, first, second)
	trial, err := second.ConfirmationRecipe(&types.ConfirmationPoCEvent{EventSequence: 0, TriggerHeight: 150})
	require.NoError(t, err)
	require.True(t, trial.Tracking)
	require.True(t, trial.PocStrongerRngEnabled)
	require.Equal(t, int64(256), trial.Models[0].Schemes[0].DecodeSteps)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 100}))
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 1))
	next, err := k.FreezePocStageRecipe(ctx, 200)
	require.NoError(t, err)
	require.Equal(t, types.PocScheme_POC_SCHEME_DECODE, next.Scheme)
	require.Equal(t, int64(99), next.Models[0].Schemes[0].DecodeSteps)
}

func TestEpochRecipeKeepsFallbackProvenance(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 2, PocStartBlockHeight: 200}))
	require.NoError(t, k.RetainedPocStageRecipe.Set(ctx, types.PocStageRecipe{StageHeight: 100, Scheme: types.PocScheme_POC_SCHEME_PREFILL}))
	require.NoError(t, k.LatestPocStageRecipe.Set(ctx, types.PocStageRecipe{StageHeight: 200, Scheme: types.PocScheme_POC_SCHEME_DECODE}))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: 2, PocStartBlockHeight: 100}))
	recipe, found, err := k.GetEpochPocRecipe(ctx, 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(100), recipe.StageHeight)
	response, err := k.PocStageRecipe(ctx, &types.QueryPocStageRecipeRequest{Lookup: &types.QueryPocStageRecipeRequest_EpochIndex{EpochIndex: 2}})
	require.NoError(t, err)
	require.Equal(t, &recipe, response.Recipe)
	round, _, err := k.GetPocStageRecipe(ctx, 200)
	require.NoError(t, err)
	require.Equal(t, types.PocScheme_POC_SCHEME_DECODE, round.Scheme, "failed round remains immutable")
}

func TestPocStageRecipeQueryRequiresExplicitLookup(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.LatestPocStageRecipe.Set(ctx, types.PocStageRecipe{StageHeight: 0}))
	for _, request := range []*types.QueryPocStageRecipeRequest{nil, {}} {
		_, err := k.PocStageRecipe(ctx, request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	// Zero is a valid stage height, not a signal to select the other lookup.
	response, err := k.PocStageRecipe(ctx, &types.QueryPocStageRecipeRequest{
		Lookup: &types.QueryPocStageRecipeRequest_StageHeight{StageHeight: 0},
	})
	require.NoError(t, err)
	require.True(t, response.Found)
	require.Equal(t, int64(0), response.Recipe.StageHeight)
}

// Epochs can carry an old measurement through several unsuccessful rounds.
func TestPocRecipeRotationRetainsEffectiveMeasurement(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	genesis, err := k.FreezePocStageRecipe(ctx, 0)
	require.NoError(t, err, "genesis freezes before an effective epoch exists")
	for epoch := uint64(0); epoch < 4; epoch++ {
		require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch, PocStartBlockHeight: int64(epoch) * 100}))
		require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: epoch, PocStartBlockHeight: 0}))
		require.NoError(t, k.EffectiveEpochIndex.Set(ctx, epoch))
		_, err = k.FreezePocStageRecipe(ctx, int64(epoch+1)*100)
		require.NoError(t, err)
		recipe, found, err := k.GetEpochPocRecipe(ctx, epoch)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, *genesis, recipe)
		if epoch > 0 {
			_, found, err = k.GetPocStageRecipe(ctx, int64(epoch)*100)
			require.NoError(t, err)
			require.False(t, found, "the previous failed attempt is evicted")
		}
	}
	// Fresh weights from the latest attempt activate. Both generations remain
	// queryable until the next regular round replaces the retained measurement.
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 4, PocStartBlockHeight: 400}))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: 4, PocStartBlockHeight: 400}))
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 4))
	active, found, err := k.GetEpochPocRecipe(ctx, 4)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(400), active.StageHeight)
	_, err = k.FreezePocStageRecipe(ctx, 500)
	require.NoError(t, err)
	retained, err := k.RetainedPocStageRecipe.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, active, retained)
	for _, height := range []int64{0, 100, 200, 300, 499, 501} {
		_, found, err = k.GetPocStageRecipe(ctx, height)
		require.NoError(t, err)
		require.False(t, found)
	}
	for _, height := range []int64{400, 500} {
		recipe, found, err := k.GetPocStageRecipe(ctx, height)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, height, recipe.StageHeight)
	}
}

func TestPocRecipeQueryUsesActiveConfirmationLifecycle(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	event := types.ConfirmationPoCEvent{
		TriggerHeight: 150,
		Recipe:        &types.PocStageRecipe{StageHeight: 150, Scheme: types.PocScheme_POC_SCHEME_DECODE},
	}
	for _, phase := range []types.ConfirmationPoCPhase{
		types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
		types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	} {
		event.Phase = phase
		require.NoError(t, k.SetConfirmationPoCEvent(ctx, event))
		require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, event))
		response, err := k.PocStageRecipe(ctx, &types.QueryPocStageRecipeRequest{Lookup: &types.QueryPocStageRecipeRequest_StageHeight{StageHeight: 150}})
		require.NoError(t, err)
		require.True(t, response.Found)
		require.Equal(t, event.Recipe, response.Recipe)
	}
	_, found, err := k.GetPocStageRecipe(ctx, 151)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, k.ClearActiveConfirmationPoCEvent(ctx))
	_, found, err = k.GetPocStageRecipe(ctx, 150)
	require.NoError(t, err)
	require.False(t, found, "historical confirmation events do not extend stage lookup retention")
	historical, found, err := k.GetConfirmationPoCEvent(ctx, event.EpochIndex, event.EventSequence)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, event.Recipe, historical.Recipe)
}
