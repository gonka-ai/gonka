package v0_2_17

import (
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeFreezesActiveEpochRecipe(t *testing.T) {
	for _, tc := range []struct {
		name              string
		measurementHeight int64
		participants      bool
	}{
		{"regular", 100, true},
		{"fallback", 50, true},
		{"without participants", 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
			params := types.DefaultParams()
			model := params.PocParams.Models[0]
			prefill := model.Schemes[0]
			model.SeqLen = prefill.SeqLen
			model.StatTest = prefill.StatTest
			model.DynamicCoefficient = prefill.DynamicCoefficient
			model.Schemes = nil
			require.NoError(t, k.SetParams(ctx, params))
			require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 5, PocStartBlockHeight: 100}))
			require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 5))
			if tc.participants {
				require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
					EpochId: 5, PocStartBlockHeight: tc.measurementHeight,
				}))
			}
			_, found, err := k.GetEpochPocRecipe(ctx, 5)
			require.NoError(t, err)
			require.False(t, found)

			handler := CreateUpgradeHandler(module.NewManager(), module.NewConfigurator(nil, nil, nil), k)
			_, err = handler(ctx, upgradetypes.Plan{Name: UpgradeName}, module.VersionMap{"capability": 1})
			require.NoError(t, err)
			recipe, found, err := k.GetEpochPocRecipe(ctx, 5)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, tc.measurementHeight, recipe.StageHeight)
			require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, recipe.Scheme)
			require.Len(t, recipe.Models, 1)
			gotPrefill, ok := recipe.Models[0].SchemeParams(types.PocScheme_POC_SCHEME_PREFILL)
			require.True(t, ok)
			require.Equal(t, prefill.SeqLen, gotPrefill.SeqLen)
			require.Equal(t, prefill.DynamicCoefficient, gotPrefill.DynamicCoefficient)
			require.Equal(t, prefill.StatTest.DistThreshold, gotPrefill.StatTest.DistThreshold)
			require.Equal(t, prefill.StatTest.PMismatch, gotPrefill.StatTest.PMismatch)
			require.Equal(t, "0.010000000000000000", decimalString(t, gotPrefill.StatTest.PValueThreshold))
			for sequence := uint64(0); sequence < 2; sequence++ {
				confirmation, err := recipe.ConfirmationRecipe(&types.ConfirmationPoCEvent{
					TriggerHeight: 150 + int64(sequence), EventSequence: sequence,
				})
				require.NoError(t, err)
				require.Equal(t, sequence == 0, confirmation.Tracking)
				if sequence == 0 {
					require.Equal(t, types.PocScheme_POC_SCHEME_DECODE, confirmation.Scheme)
				} else {
					require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, confirmation.Scheme)
				}
			}
			live, err := k.GetParams(ctx)
			require.NoError(t, err)
			require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, live.PocParams.PocScheme)

			live.PocParams.Models[0].Schemes[0].SeqLen = 2048
			require.NoError(t, k.SetParams(ctx, live))
			require.NoError(t, freezeActiveEpochRecipe(ctx, k))
			repeated, found, err := k.GetEpochPocRecipe(ctx, 5)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, recipe, repeated, "existing recipe must not be overwritten")

			_, err = k.FreezePocStageRecipe(ctx, 200)
			require.NoError(t, err)
			retained, err := k.RetainedPocStageRecipe.Get(ctx)
			require.NoError(t, err)
			require.Equal(t, recipe, retained, "the first post-upgrade round retains the active measurement")
		})
	}
}
