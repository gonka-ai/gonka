package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func testDynamicPocParams() *PocParams {
	return &PocParams{
		Models: []*PoCModelConfig{
			{
				ModelId:           "a",
				WeightScaleFactor: &Decimal{Value: 1, Exponent: 0},
				DynamicCoefficient: &DynamicCoefficientModelConfig{
					CoeffMin:           &Decimal{Value: 9, Exponent: -1},
					CoeffMax:           &Decimal{Value: 11, Exponent: -1},
					RelativeDifficulty: &Decimal{Value: 1, Exponent: 0},
					TargetShareBps:     5000,
				},
			},
			{
				ModelId:           "b",
				WeightScaleFactor: &Decimal{Value: 1, Exponent: 0},
				DynamicCoefficient: &DynamicCoefficientModelConfig{
					CoeffMin:           &Decimal{Value: 1, Exponent: 0},
					CoeffMax:           &Decimal{Value: 1, Exponent: 0},
					RelativeDifficulty: &Decimal{Value: 1, Exponent: 0},
					TargetShareBps:     5000,
				},
			},
		},
		DynamicCoefficientParams: &DynamicCoefficientParams{
			TargetZoneBps:     500,
			StepMin:           &Decimal{Value: 5, Exponent: -3},
			StepMax:           &Decimal{Value: 5, Exponent: -2},
			BootstrapStepMax:  &Decimal{Value: 25, Exponent: -2},
			BootstrapShareBps: 100,
		},
	}
}

func TestDynamicCoefficientParamsValidate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		require.NoError(t, testDynamicPocParams().Validate())
	})

	t.Run("config presence defines enabled models", func(t *testing.T) {
		params := testDynamicPocParams()
		params.Models[0].DynamicCoefficient = nil
		params.Models[1].DynamicCoefficient.TargetShareBps = 10000
		require.NoError(t, params.Validate())
	})

	t.Run("targets sum exactly", func(t *testing.T) {
		params := testDynamicPocParams()
		params.Models[0].DynamicCoefficient.TargetShareBps = 4999
		require.ErrorContains(t, params.Validate(), "must sum to 10000")
	})

	t.Run("target must exceed inclusive zone", func(t *testing.T) {
		params := testDynamicPocParams()
		params.DynamicCoefficientParams.TargetZoneBps = 5000
		require.ErrorContains(t, params.Validate(), "must be greater than target_zone_bps")
	})

	t.Run("coeff min must be positive", func(t *testing.T) {
		params := testDynamicPocParams()
		params.Models[0].DynamicCoefficient.CoeffMin = &Decimal{Value: 0, Exponent: 0}
		require.ErrorContains(t, params.Validate(), "must be positive")
	})

	t.Run("disabled model may omit config", func(t *testing.T) {
		params := testDynamicPocParams()
		params.Models = append(params.Models, &PoCModelConfig{
			ModelId: "disabled",
		})
		require.NoError(t, params.Validate())
	})
}

func TestPocParamsCannotRemoveDynamicConfigAfterMigration(t *testing.T) {
	params := &PocParams{
		Models: []*PoCModelConfig{{ModelId: "model-a"}},
	}
	require.ErrorContains(t, params.Validate(), "cannot be nil after deprecated weight_scale_factor was removed")

	params.Models[0].WeightScaleFactor = &Decimal{Value: 1, Exponent: 0}
	require.NoError(t, params.Validate())
}
