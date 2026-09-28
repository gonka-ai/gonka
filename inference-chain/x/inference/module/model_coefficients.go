package inference

import (
	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

func modelCoefficients(pocParams *types.PocParams) map[string]mathsdk.LegacyDec {
	coeffs := make(map[string]mathsdk.LegacyDec)
	if pocParams == nil {
		return coeffs
	}
	for _, config := range pocParams.GetModelConfigs() {
		if config != nil && config.ModelId != "" {
			coeffs[config.ModelId] = config.GetWeightScaleFactorDec()
		}
	}
	return coeffs
}

// modelCoefficientsFromRecipe uses the scale on the frozen scheme block.
// A stage that copied only the DECODE block must not fall back to the prefill factor.
func modelCoefficientsFromRecipe(recipe *types.PocStageRecipe) map[string]mathsdk.LegacyDec {
	coeffs := make(map[string]mathsdk.LegacyDec)
	if recipe == nil {
		return coeffs
	}
	for _, config := range recipe.Models {
		if config == nil || config.ModelId == "" {
			continue
		}
		block, ok := config.SchemeParams(recipe.Scheme)
		if !ok || block == nil {
			continue
		}
		coeffs[config.ModelId] = block.WeightScaleFactor.LegacyDecOrOne()
	}
	return coeffs
}
