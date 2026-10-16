package types

// DECODE templates are independent of PREFILL settings. Initial values were
// copied from node3.gonka.ai's params for tuning before activation; they are not
// calibrated DECODE thresholds. Input/output lengths are 256/256.
var decodePoCTemplates = map[string]struct {
	distance, minimum, maximum, difficulty float64
	share                                  uint32
}{
	"MiniMaxAI/MiniMax-M2.7":             {0.75, 0.3024, 0.3024, 1, 3334},
	"deepseek-ai/DeepSeek-V4-Flash-0731": {0.41, 0.2214, 0.2706, 1.541666666666, 3333},
	"zai-org/GLM-5.3-Flash":              {0.44, 0.558, 0.682, 2.668169014084, 3333},
}

// DefaultDecodeScheme returns a fresh block, also usable for local genesis.
func DefaultDecodeScheme(modelID string) *PocSchemeParams {
	t, ok := decodePoCTemplates[modelID]
	if !ok {
		t.distance, t.minimum, t.maximum, t.difficulty, t.share = 0.4, 1, 1, 1, 10000
	}
	return &PocSchemeParams{
		Scheme: PocScheme_POC_SCHEME_DECODE, SeqLen: 256, MaxTokens: 256,
		StatTest: &PoCStatTestParams{
			DistThreshold:   DecimalFromFloat(t.distance),
			PMismatch:       DecimalFromFloat(0.1),
			PValueThreshold: DecimalFromFloat(0.05),
		},
		DynamicCoefficient: &DynamicCoefficientModelConfig{
			CoeffMin: DecimalFromFloat(t.minimum), CoeffMax: DecimalFromFloat(t.maximum),
			RelativeDifficulty: DecimalFromFloat(t.difficulty), TargetShareBps: t.share,
		},
	}
}
