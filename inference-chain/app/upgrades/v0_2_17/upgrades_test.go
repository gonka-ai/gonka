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
	params.PocParams.ConfirmationPocScheme = inferencetypes.PocScheme_POC_SCHEME_PREFILL
	params.PocParams.ConfirmationSchemeEvents = 0
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
	require.Equal(t, inferencetypes.PocScheme_POC_SCHEME_PREFILL, got.PocParams.ConfirmationPocScheme)
	require.Equal(t, uint32(0), got.PocParams.ConfirmationSchemeEvents)
	model := got.PocParams.Models[0]
	require.Equal(t, int64(0), model.SeqLen)
	require.Nil(t, model.StatTest)
	require.Nil(t, model.WeightScaleFactor)
	require.Nil(t, model.DynamicCoefficient)
	require.Equal(t, uint64(4), model.PenaltyStartEpoch)
	require.Len(t, model.Schemes, 1)
	block := model.Schemes[0]
	require.Equal(t, inferencetypes.PocScheme_POC_SCHEME_PREFILL, block.Scheme)
	require.Equal(t, int64(128), block.SeqLen)
	require.Equal(t, int64(0), block.MaxTokens)
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
	require.Len(t, model.Schemes, 1)
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
