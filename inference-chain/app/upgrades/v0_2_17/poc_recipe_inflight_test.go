package v0_2_17

import (
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// The upgrade height can land after v0.2.16 started a regular PoC stage and
// before that stage's epoch becomes effective. The stage needs a recipe too.
func TestUpgradeFreezesInFlightRegularStage(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params := types.DefaultParams()
	model := params.PocParams.Models[0]
	prefill := model.Schemes[0]
	model.SeqLen = prefill.SeqLen
	model.StatTest = prefill.StatTest
	model.DynamicCoefficient = prefill.DynamicCoefficient
	model.Schemes = nil
	require.NoError(t, k.SetParams(ctx, params))

	// Epoch 5 is effective and was measured at height 100.
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 5, PocStartBlockHeight: 100}))
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 5))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: 5, PocStartBlockHeight: 100}))
	// v0.2.16 started the regular PoC for epoch 6 at height 200.
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 6, PocStartBlockHeight: 200}))

	handler := CreateUpgradeHandler(module.NewManager(), module.NewConfigurator(nil, nil, nil), k)
	_, err := handler(ctx, upgradetypes.Plan{Name: UpgradeName}, module.VersionMap{"capability": 1})
	require.NoError(t, err)

	active, found, err := k.GetEpochPocRecipe(ctx, 5)
	require.NoError(t, err)
	require.True(t, found, "the effective epoch keeps its recipe")
	require.Equal(t, int64(100), active.StageHeight)

	inFlight, found, err := k.GetPocStageRecipe(ctx, 200)
	require.NoError(t, err)
	require.True(t, found, "validators read the in-flight stage by its start height")
	require.Equal(t, int64(200), inFlight.StageHeight)
	require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, inFlight.Scheme)
	gotPrefill, ok := inFlight.Models[0].SchemeParams(types.PocScheme_POC_SCHEME_PREFILL)
	require.True(t, ok)
	require.Equal(t, prefill.SeqLen, gotPrefill.SeqLen)

	// Epoch 6 forms from that stage and becomes effective.
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: 6, PocStartBlockHeight: 200}))
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 6))

	// PoC start of epoch 7, the call EndBlock makes and treats as unrecoverable.
	_, err = k.FreezePocStageRecipe(ctx, 300)
	require.NoError(t, err)
	retained, err := k.RetainedPocStageRecipe.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, inFlight, retained)
}
