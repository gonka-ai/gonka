package calculations

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMinimumValidationAverage(t *testing.T) {
	tests := []struct {
		testName                 string
		recentRequestCount       int64
		fullValidationCutoff     int64
		minimumValidationCutoff  int64
		maxValidationAverage     float64
		halfwayValidationAverage float64
		minimumValidationAverage float64
		expectedAverage          decimal.Decimal
	}{
		{
			testName:                 "maximum traffic",
			recentRequestCount:       10_000,
			fullValidationCutoff:     10_000,
			minimumValidationCutoff:  100,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromFloat(0.01),
		},
		{
			testName:                 "minimum traffic",
			recentRequestCount:       100,
			fullValidationCutoff:     10_000,
			minimumValidationCutoff:  100,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromInt(1),
		},
		{
			testName:                 "halfway traffic",
			recentRequestCount:       5_000,
			fullValidationCutoff:     10_000,
			minimumValidationCutoff:  100,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromFloat(0.05),
		},
		{
			testName:                 "halfway of halfway traffic",
			recentRequestCount:       7_500,
			fullValidationCutoff:     10_000,
			minimumValidationCutoff:  100,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromFloat(0.03),
		},
		{
			testName:                 "25 percent of halfway traffic",
			recentRequestCount:       6_250,
			fullValidationCutoff:     10_000,
			minimumValidationCutoff:  100,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromFloat(0.04),
		},
		{
			testName:                 "below halfway, mid",
			recentRequestCount:       2_550,
			fullValidationCutoff:     10_000,
			minimumValidationCutoff:  100,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromFloat(0.525),
		},
		{
			testName:                 "below halfway, 75%",
			recentRequestCount:       3_775,
			fullValidationCutoff:     10_000,
			minimumValidationCutoff:  100,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromFloat(0.2875),
		},
		{
			testName:                 "below halfway, mid, 1/10th scale",
			recentRequestCount:       255,
			fullValidationCutoff:     1000,
			minimumValidationCutoff:  10,
			maxValidationAverage:     1.0,
			halfwayValidationAverage: 0.05,
			minimumValidationAverage: 0.01,
			expectedAverage:          decimal.NewFromFloat(0.525),
		},
	}
	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			testParams := &types.ValidationParams{
				FullValidationTrafficCutoff: tt.fullValidationCutoff,
				MinValidationHalfway:        types.DecimalFromFloat(tt.halfwayValidationAverage),
				MinValidationAverage:        types.DecimalFromFloat(tt.minimumValidationAverage),
				MaxValidationAverage:        types.DecimalFromFloat(tt.maxValidationAverage),
				MinValidationTrafficCutoff:  tt.minimumValidationCutoff,
			}
			result := CalculateMinimumValidationAverage(tt.recentRequestCount, testParams)
			require.True(t, tt.expectedAverage.Equal(result))
		})
	}
}
