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
	next, err := k.FreezePocStageRecipe(ctx, 200)
	require.NoError(t, err)
	require.Equal(t, types.PocScheme_POC_SCHEME_DECODE, next.Scheme)
	require.Equal(t, int64(99), next.Models[0].Schemes[0].DecodeSteps)
}

func TestEpochRecipeKeepsFallbackProvenance(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 2, PocStartBlockHeight: 200}))
	require.NoError(t, k.PocStageRecipes.Set(ctx, 100, types.PocStageRecipe{StageHeight: 100, Scheme: types.PocScheme_POC_SCHEME_PREFILL}))
	require.NoError(t, k.PocStageRecipes.Set(ctx, 200, types.PocStageRecipe{StageHeight: 200, Scheme: types.PocScheme_POC_SCHEME_DECODE}))
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
	require.NoError(t, k.PocStageRecipes.Set(ctx, 0, types.PocStageRecipe{StageHeight: 0}))
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
