package keeper_test

import (
	"testing"

	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestSetParams_PocSchemeChangeSetsGraceEpoch(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 5, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))

	params := types.DefaultParams()
	require.NoError(t, k.SetParams(ctx, params))
	_, found := k.GetPocSchemeEnabledEpoch(ctx)
	require.False(t, found, "storing PREFILL when store was empty must not set grace")

	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_DECODE
	require.NoError(t, k.SetParams(ctx, params))
	epoch, found := k.GetPocSchemeEnabledEpoch(ctx)
	require.True(t, found)
	require.Equal(t, uint64(5), epoch)

	require.NoError(t, k.SetParams(ctx, params))
	epoch, found = k.GetPocSchemeEnabledEpoch(ctx)
	require.True(t, found)
	require.Equal(t, uint64(5), epoch, "no-op SetParams must not reset grace")

	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 6, PocStartBlockHeight: 200}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 6))
	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_PREFILL
	require.NoError(t, k.SetParams(ctx, params))
	epoch, found = k.GetPocSchemeEnabledEpoch(ctx)
	require.True(t, found)
	require.Equal(t, uint64(6), epoch, "DECODE→PREFILL must set grace again")
}

func TestFreezePocStageRecipe_WriteOnce(t *testing.T) {
	k, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)
	params := types.DefaultParams()
	params.PocParams.PocStrongerRngEnabled = true
	require.NoError(t, k.SetParams(ctx, params))

	first, err := k.FreezePocStageRecipe(ctx, 100, nil)
	require.NoError(t, err)
	require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, first.Scheme)
	require.True(t, first.PocStrongerRngEnabled)

	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_DECODE
	params.PocParams.PocStrongerRngEnabled = false
	params.PocParams.ConfirmationPocScheme = types.PocScheme_POC_SCHEME_DECODE
	params.PocParams.ConfirmationSchemeEvents = 1
	require.NoError(t, k.SetParams(ctx, params))

	second, err := k.FreezePocStageRecipe(ctx, 100, nil)
	require.NoError(t, err)
	require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, second.Scheme)
	require.True(t, second.PocStrongerRngEnabled)

	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_PREFILL
	params.PocParams.ConfirmationPocScheme = types.PocScheme_POC_SCHEME_DECODE
	params.PocParams.ConfirmationSchemeEvents = 1
	require.NoError(t, k.SetParams(ctx, params))

	canary, err := k.FreezePocStageRecipe(ctx, 200, &types.ConfirmationPoCEvent{EventSequence: 0, TriggerHeight: 200})
	require.NoError(t, err)
	require.Equal(t, types.PocScheme_POC_SCHEME_DECODE, canary.Scheme)
	require.True(t, canary.Tracking)
}
