package types

// DefaultDecodeScheme returns a fresh, model-independent decode configuration.
func DefaultDecodeScheme() *PocSchemeParams {
	return &PocSchemeParams{
		Scheme:    PocScheme_POC_SCHEME_DECODE,
		SeqLen:    256,
		MaxTokens: 256,
		StatTest: &PoCStatTestParams{
			DistThreshold:   DecimalFromFloat(0.4),
			PMismatch:       DecimalFromFloat(0.1),
			PValueThreshold: DecimalFromFloat(0.05),
		},
		DynamicCoefficient: &DynamicCoefficientModelConfig{
			CoeffMin:           DecimalFromFloat(1),
			CoeffMax:           DecimalFromFloat(1),
			RelativeDifficulty: DecimalFromFloat(1),
			TargetShareBps:     10000,
		},
	}
}
