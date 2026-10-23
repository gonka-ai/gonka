package v0_2_17

import (
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

// DECODE dist_threshold is the claimed-margin tolerance (tau), not PREFILL L2 distance.
var decodePoCTemplates = map[string]*types.PocSchemeParams{
	"MiniMaxAI/MiniMax-M2.7": {
		StatTest: &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.0395)},
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           types.DecimalFromFloat(0.3024),
			CoeffMax:           types.DecimalFromFloat(0.3024),
			RelativeDifficulty: types.DecimalFromFloat(1),
			TargetShareBps:     3334,
		},
	},
	"deepseek-ai/DeepSeek-V4-Flash-0731": {
		StatTest: &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.027)},
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           types.DecimalFromFloat(0.2012461916454),
			CoeffMax:           types.DecimalFromFloat(0.672672),
			RelativeDifficulty: types.DecimalFromFloat(1.925925925925),
			TargetShareBps:     3333,
		},
	},
	"zai-org/GLM-5.3-Flash": {
		StatTest: &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.017)},
		DynamicCoefficient: &types.DynamicCoefficientModelConfig{
			CoeffMin:           types.DecimalFromFloat(0.810962376237),
			CoeffMax:           types.DecimalFromFloat(1.9118046315786),
			RelativeDifficulty: types.DecimalFromFloat(5.473684210526),
			TargetShareBps:     3333,
		},
	},
}

// pinFraudAlpha stores the alpha MLNode has been applying. It reads
// fraud_threshold and dropped the chain's p_value_threshold, so mainnet
// validated at 0.01 while the stored value stayed 0.05. v0.2.17 sends that
// chain field under the name MLNode reads; leaving 0.05 would move the fraud
// cut at the upgrade. Distances and p_mismatch are left as stored.
func pinFraudAlpha(stat *types.PoCStatTestParams) *types.PoCStatTestParams {
	if stat == nil {
		return nil
	}
	out := gogoproto.Clone(stat).(*types.PoCStatTestParams)
	out.PValueThreshold = types.DecimalFromFloat(0.01)
	return out
}

func decodeScheme(modelID string) *types.PocSchemeParams {
	template, ok := decodePoCTemplates[modelID]
	if !ok {
		block := types.DefaultDecodeScheme()
		block.StatTest = pinFraudAlpha(block.StatTest)
		return block
	}
	block := gogoproto.Clone(template).(*types.PocSchemeParams)
	block.Scheme = types.PocScheme_POC_SCHEME_DECODE
	block.SeqLen = 256
	block.DecodeSteps = 256
	block.StatTest.PMismatch = types.DecimalFromFloat(0.1)
	block.StatTest = pinFraudAlpha(block.StatTest)
	return block
}
