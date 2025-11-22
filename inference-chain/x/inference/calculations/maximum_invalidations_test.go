package calculations

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func d(val float64) decimal.Decimal {
	return decimal.NewFromFloat(val)
}

func TestCalculateInvalidationsTanh(t *testing.T) {
	tests := []struct {
		name             string
		inferences       int64
		weight           decimal.Decimal
		reputation       int32
		maxInvalidations int64
		minInvalidations int64
		curveFactor      int64
		expectedMin      int64
		expectedMax      int64
	}{
		{
			name:       "Low inference baseline",
			inferences: 10, weight: d(0.1), reputation: 50,
			maxInvalidations: 1000, curveFactor: 500,
			expectedMin: 1, expectedMax: 5,
		},
		{
			name:       "Mid-level actor",
			inferences: 500, weight: d(0.2), reputation: 80,
			maxInvalidations: 1000, curveFactor: 500,
			expectedMin: 20, expectedMax: 200,
		},
		{
			name:       "High power whale",
			inferences: 1000, weight: d(0.3), reputation: 100,
			maxInvalidations: 1000, curveFactor: 500,
			expectedMin: 250, expectedMax: 400,
		},
		{
			name:       "Minimum guaranteed invalidation",
			inferences: 0, weight: d(0.001), reputation: 1,
			maxInvalidations: 1000, curveFactor: 500,
			expectedMin: 1, expectedMax: 1,
		},
		{
			name:       "Minimum guaranteed invalidations set",
			inferences: 0, weight: d(0.001), reputation: 1,
			maxInvalidations: 1000, curveFactor: 500,
			expectedMin: 2, expectedMax: 2,
			minInvalidations: 2,
		},
		{
			name:       "Capped by max invalidations",
			inferences: 10000, weight: d(1.0), reputation: 100,
			maxInvalidations: 1000, curveFactor: 500,
			expectedMin: 950, expectedMax: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateInvalidations(
				tt.inferences,
				tt.weight,
				tt.reputation,
				tt.maxInvalidations,
				tt.curveFactor,
				tt.minInvalidations,
			)
			assert.GreaterOrEqual(t, result, tt.expectedMin, "Too few invalidations")
			assert.LessOrEqual(t, result, tt.expectedMax, "Too many invalidations")
		})
	}
}
