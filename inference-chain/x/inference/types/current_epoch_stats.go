package types

func NewCurrentEpochStats() *CurrentEpochStats {
	return &CurrentEpochStats{
		InvalidLLR: &Decimal{
			Value:    0,
			Exponent: 0,
		},
		InactiveLLR: &Decimal{
			Value:    0,
			Exponent: 0,
		},
	}
}
