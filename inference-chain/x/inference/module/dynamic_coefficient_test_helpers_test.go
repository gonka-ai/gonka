package inference

import "github.com/productscience/inference/x/inference/types"

func dec(value int64, exponent int32) *types.Decimal {
	return &types.Decimal{Value: value, Exponent: exponent}
}

func dynamicModel(
	id string,
	weightScale *types.Decimal,
	min *types.Decimal,
	max *types.Decimal,
	difficulty *types.Decimal,
	target uint32,
) *types.PoCModelConfig {
	return &types.PoCModelConfig{
		ModelId:           id,
		WeightScaleFactor: weightScale,
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           min,
			CoeffMax:           max,
			RelativeDifficulty: difficulty,
			TargetShareBps:     target,
		},
	}
}

func dynamicPocParams(models ...*types.PoCModelConfig) *types.PocParams {
	return &types.PocParams{
		Models: models,
		DynamicCoefficientParams: &types.DynamicCoefficientParams{
			TargetZoneBps:     500,
			StepMin:           dec(5, -3),
			StepMax:           dec(5, -2),
			BootstrapStepMax:  dec(25, -2),
			BootstrapShareBps: 100,
		},
	}
}
