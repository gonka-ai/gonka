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
