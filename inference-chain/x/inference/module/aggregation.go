package inference

import (
	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// ModelWeight holds the raw proven compute weight for one model.
// PocWeight on MLNodeInfo is always raw (before any coefficient).
type ModelWeight struct {
	ModelID   string
	RawWeight int64
}

// SumNodeWeights returns total raw PocWeight for a set of nodes.
func SumNodeWeights(nodes []*types.MLNodeInfo) int64 {
	total := int64(0)
	for _, node := range nodes {
		if node != nil {
			total += node.PocWeight
		}
	}
	return total
}

// ExtractModelWeights reads per-model raw weights from a participant.
// Requires Models and MlNodes to be parallel arrays (same index = same model).
func ExtractModelWeights(p *types.ActiveParticipant) []ModelWeight {
	if p == nil {
		return nil
	}
	weights := make([]ModelWeight, 0, len(p.Models))
	for i, modelId := range p.Models {
		if i < len(p.MlNodes) && p.MlNodes[i] != nil {
			weights = append(weights, ModelWeight{
				ModelID:   modelId,
				RawWeight: SumNodeWeights(p.MlNodes[i].MlNodes),
			})
		}
	}
	return weights
}

// ModelCoefficients extracts per-model coefficients from params.
// Currently returns WeightScaleFactor for each model (stub for design-2 consensus coefficients).
func ModelCoefficients(pocParams *types.PocParams) map[string]mathsdk.LegacyDec {
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

// AggregateConsensusWeight is the ONE place where per-model weights combine.
// consensusWeight(p) = sum(coeff_i * rawWeight_i)
// Models without a coefficient use 1.0.
func AggregateConsensusWeight(modelWeights []ModelWeight, coefficients map[string]mathsdk.LegacyDec) int64 {
	total := int64(0)
	for _, mw := range modelWeights {
		coeff, ok := coefficients[mw.ModelID]
		if !ok {
			coeff = mathsdk.LegacyOneDec()
		}
		total += coeff.MulInt64(mw.RawWeight).TruncateInt64()
	}
	return total
}
