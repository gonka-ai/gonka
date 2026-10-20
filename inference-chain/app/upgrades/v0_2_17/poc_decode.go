package v0_2_17

import (
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

// Statistical thresholds remain initial values pending decode calibration.
var decodePoCTemplates = map[string]*types.PocSchemeParams{
	"MiniMaxAI/MiniMax-M2.7": {
		StatTest: &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.75)},
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           types.DecimalFromFloat(0.3024),
			CoeffMax:           types.DecimalFromFloat(0.3024),
			RelativeDifficulty: types.DecimalFromFloat(1),
			TargetShareBps:     3334,
		},
	},
	"deepseek-ai/DeepSeek-V4-Flash-0731": {
		StatTest: &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.41)},
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           types.DecimalFromFloat(0.2012461916454),
			CoeffMax:           types.DecimalFromFloat(0.672672),
			RelativeDifficulty: types.DecimalFromFloat(1.925925925925),
			TargetShareBps:     3333,
		},
	},
	"zai-org/GLM-5.3-Flash": {
		StatTest: &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.44)},
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           types.DecimalFromFloat(0.810962376237),
			CoeffMax:           types.DecimalFromFloat(1.9118046315786),
			RelativeDifficulty: types.DecimalFromFloat(5.473684210526),
			TargetShareBps:     3333,
		},
	},
}

func decodeScheme(modelID string) *types.PocSchemeParams {
	template, ok := decodePoCTemplates[modelID]
	if !ok {
		return types.DefaultDecodeScheme()
	}
	block := gogoproto.Clone(template).(*types.PocSchemeParams)
	block.Scheme = types.PocScheme_POC_SCHEME_DECODE
	block.SeqLen = 256
	block.MaxTokens = 256
	block.StatTest.PMismatch = types.DecimalFromFloat(0.1)
	block.StatTest.PValueThreshold = types.DecimalFromFloat(0.05)
	return block
}
