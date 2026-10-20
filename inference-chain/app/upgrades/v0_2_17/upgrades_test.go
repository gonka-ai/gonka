package v0_2_17

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.17", UpgradeName)
}

// A post-v0.2.16 model keeps seq_len and stat_test, stores the controller on
// the model, and has a cleared weight_scale_factor. v0.2.17 moves both onto
// one PREFILL block and leaves the scheme selector alone.
func TestMigratePocSchemeBlocksFromDynamicCoefficient(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	stat := &inferencetypes.PoCStatTestParams{DistThreshold: inferencetypes.DecimalFromFloat(0.41)}
	coefficient := &inferencetypes.DynamicCoefficientModelConfig{
		CoeffMin:           &inferencetypes.Decimal{Value: 15, Exponent: -1},
		CoeffMax:           &inferencetypes.Decimal{Value: 15, Exponent: -1},
		RelativeDifficulty: &inferencetypes.Decimal{Value: 1, Exponent: 0},
		TargetShareBps:     10000,
	}
	params.PocParams.PocScheme = inferencetypes.PocScheme_POC_SCHEME_PREFILL
	params.PocParams.DynamicCoefficientParams = &inferencetypes.DynamicCoefficientParams{
		TargetZoneBps:     500,
		StepMin:           inferencetypes.DecimalFromFloat(0.005),
		StepMax:           inferencetypes.DecimalFromFloat(0.05),
		BootstrapStepMax:  inferencetypes.DecimalFromFloat(0.25),
		BootstrapShareBps: 100,
	}
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId:            "model-a",
		SeqLen:             128,
		StatTest:           stat,
		PenaltyStartEpoch:  4,
		DynamicCoefficient: coefficient,
	}}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migratePocSchemeBlocks(ctx, k))
	require.NoError(t, migratePocSchemeBlocks(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.PocScheme_POC_SCHEME_PREFILL, got.PocParams.PocScheme)
	model := got.PocParams.Models[0]
	require.Equal(t, int64(0), model.SeqLen)
	require.Nil(t, model.StatTest)
	require.Nil(t, model.WeightScaleFactor)
	require.Nil(t, model.DynamicCoefficient)
	require.Equal(t, uint64(4), model.PenaltyStartEpoch)
	require.Len(t, model.Schemes, 2)
	block := model.Schemes[0]
	require.Equal(t, inferencetypes.PocScheme_POC_SCHEME_PREFILL, block.Scheme)
	require.Equal(t, int64(128), block.SeqLen)
	require.Equal(t, int64(0), block.DecodeSteps)
	require.Equal(t, stat, block.StatTest)
	require.Equal(t, coefficient, block.DynamicCoefficient)
	require.NoError(t, got.Validate())
}

// Flat pre-decode params have no dynamic controller. The recipe moves onto the
// PREFILL block and the static weight_scale_factor stays on the model.
func TestMigratePocSchemeBlocksFromFlatWeight(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	weight := inferencetypes.DecimalFromFloat(1.5)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId:           "model-a",
		SeqLen:            128,
		WeightScaleFactor: weight,
	}}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migratePocSchemeBlocks(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	model := got.PocParams.Models[0]
	require.Equal(t, weight, model.WeightScaleFactor)
	require.Nil(t, model.DynamicCoefficient)
	require.Equal(t, int64(0), model.SeqLen)
	require.Len(t, model.Schemes, 2)
	require.Nil(t, model.Schemes[0].DynamicCoefficient)
	require.Equal(t, int64(128), model.Schemes[0].SeqLen)
	require.NoError(t, got.Validate())
}

func TestMigratePocSchemeBlocksRejectsMissingRecipe(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId:           "model-a",
		WeightScaleFactor: inferencetypes.DecimalFromFloat(1),
	}}
	require.NoError(t, k.SetParams(ctx, params))

	err = migratePocSchemeBlocks(ctx, k)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no PREFILL scheme block")

	got, getErr := k.GetParams(ctx)
	require.NoError(t, getErr)
	require.Empty(t, got.PocParams.Models[0].Schemes)
}

func TestPostV016ParamsRoundTripModelCoefficient(t *testing.T) {
	stat := &inferencetypes.PoCStatTestParams{DistThreshold: inferencetypes.DecimalFromFloat(0.41)}
	coefficient := &inferencetypes.DynamicCoefficientModelConfig{
		CoeffMin:           &inferencetypes.Decimal{Value: 15, Exponent: -1},
		CoeffMax:           &inferencetypes.Decimal{Value: 15, Exponent: -1},
		RelativeDifficulty: &inferencetypes.Decimal{Value: 1, Exponent: 0},
		TargetShareBps:     10000,
	}
	src := &inferencetypes.PocParams{
		Models: []*inferencetypes.PoCModelConfig{{
			ModelId:            "model-a",
			SeqLen:             128,
			StatTest:           stat,
			DynamicCoefficient: coefficient,
		}},
		DynamicCoefficientParams: &inferencetypes.DynamicCoefficientParams{
			TargetZoneBps:     500,
			StepMin:           inferencetypes.DecimalFromFloat(0.005),
			StepMax:           inferencetypes.DecimalFromFloat(0.05),
			BootstrapStepMax:  inferencetypes.DecimalFromFloat(0.25),
			BootstrapShareBps: 100,
		},
	}
	bz, err := src.Marshal()
	require.NoError(t, err)

	var decoded inferencetypes.PocParams
	require.NoError(t, decoded.Unmarshal(bz))
	require.Equal(t, int64(128), decoded.Models[0].SeqLen)
	require.Equal(t, coefficient, decoded.Models[0].DynamicCoefficient)
	require.Empty(t, decoded.Models[0].Schemes)
	require.NotNil(t, decoded.DynamicCoefficientParams)
}

func TestMigratePocSchemeBlocksInstallsDecodeConfig(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	cases := []struct {
		id, minimum, maximum, difficulty, tau string
		share                                 uint32
	}{
		{"MiniMaxAI/MiniMax-M2.7", "0.302400000000000000", "0.302400000000000000", "1.000000000000000000", "0.039500000000000000", 3334},
		{"deepseek-ai/DeepSeek-V4-Flash-0731", "0.201246191645400000", "0.672672000000000000", "1.925925925925000000", "0.027000000000000000", 3333},
		{"zai-org/GLM-5.3-Flash", "0.810962376237000000", "1.911804631578600000", "5.473684210526000000", "0.017000000000000000", 3333},
	}
	params.PocParams.Models = nil
	params.PocParams.PocScheme = inferencetypes.PocScheme_POC_SCHEME_PREFILL
	for _, tc := range cases {
		params.PocParams.Models = append(params.PocParams.Models, &inferencetypes.PoCModelConfig{
			ModelId: tc.id, SeqLen: 1024, StatTest: inferencetypes.DefaultPoCStatTestParams(),
			DynamicCoefficient: &inferencetypes.DynamicCoefficientModelConfig{
				CoeffMin: inferencetypes.DecimalFromFloat(1), CoeffMax: inferencetypes.DecimalFromFloat(2),
				RelativeDifficulty: inferencetypes.DecimalFromFloat(1), TargetShareBps: tc.share,
			},
		})
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, migratePocSchemeBlocks(ctx, k))
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, got.Validate())
	require.Equal(t, inferencetypes.PocScheme_POC_SCHEME_PREFILL, got.PocParams.PocScheme)
	for i, tc := range cases {
		model := got.PocParams.Models[i]
		prefill, ok := model.SchemeParams(inferencetypes.PocScheme_POC_SCHEME_PREFILL)
		require.True(t, ok)
		require.Equal(t, params.PocParams.Models[i].DynamicCoefficient, prefill.DynamicCoefficient)
		require.Equal(t, params.PocParams.Models[i].StatTest, prefill.StatTest)
		decode, ok := model.SchemeParams(inferencetypes.PocScheme_POC_SCHEME_DECODE)
		require.True(t, ok)
		require.Equal(t, int64(256), decode.SeqLen)
		require.Equal(t, int64(256), decode.DecodeSteps)
		for _, value := range []struct {
			got  *inferencetypes.Decimal
			want string
		}{
			{decode.StatTest.DistThreshold, tc.tau},
			{decode.StatTest.PMismatch, "0.100000000000000000"},
			{decode.StatTest.PValueThreshold, "0.050000000000000000"},
			{decode.DynamicCoefficient.CoeffMin, tc.minimum},
			{decode.DynamicCoefficient.CoeffMax, tc.maximum},
			{decode.DynamicCoefficient.RelativeDifficulty, tc.difficulty},
		} {
			actual, err := value.got.ToLegacyDec()
			require.NoError(t, err)
			require.Equal(t, value.want, actual.String(), tc.id)
		}
		require.Equal(t, tc.share, decode.DynamicCoefficient.TargetShareBps)
	}
	require.NoError(t, migratePocSchemeBlocks(ctx, k))
	repeated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, got, repeated)
}
