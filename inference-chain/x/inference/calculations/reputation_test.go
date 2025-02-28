package calculations

import (
	"fmt"
	"github.com/shopspring/decimal"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const (
	fixedInferenceId = "inferenceId"
	// Given fixedInferenceId, these seeds will produce close (slightly higher) to all of these probabilities
	ninetyPercentSeed    = int64(5798067479865859744)
	fiftyPercentSeed     = int64(6669939700021626378)
	tenPercentSeed       = int64(2925341513999858939)
	defaultTrafficCutoff = 10_000
	defaultEpochsToMax   = 30
)

// ExtractValidationDetails parses and extracts values from a message.
func ExtractValidationDetails(msg string) (shouldValidate bool, randFloat float64, ourProbability float64, err error) {
	// Define the layout to match the expected string format
	_, err = fmt.Sscanf(msg, "Should Validate: %t randFloat: %f ourProbability: %f", &shouldValidate, &randFloat, &ourProbability)
	return
}

func TestCalculateReputation(t *testing.T) {
	tests := []struct {
		testName        string
		epochCount      int64
		epochsToMax     int64
		missPercentages []float64
		expected        int64
	}{
		{
			testName:    "no epochs",
			epochCount:  0,
			epochsToMax: 30,
			expected:    0,
		},
		{
			testName:    "halfway",
			epochCount:  15,
			epochsToMax: 30,
			expected:    50,
		},
		{
			testName:    "max",
			epochCount:  30,
			epochsToMax: 30,
			expected:    100,
		},
		{
			testName:    "one third (trunc to 2 decimal places)",
			epochCount:  10,
			epochsToMax: 30,
			expected:    33,
		},
		{
			testName:    "two thirds (trunc to 2 decimal places)",
			epochCount:  20,
			epochsToMax: 30,
			expected:    66,
		},
		{
			testName:        "max, but with one half missed",
			epochCount:      10,
			epochsToMax:     10,
			missPercentages: []float64{0.5},
			expected:        95,
		},
		{
			testName:        "max, but with many missed",
			epochCount:      10,
			epochsToMax:     10,
			missPercentages: []float64{0.25, 0.5, 0.5, 0.5, 0.75, 0.5},
			expected:        70,
		},
		{
			testName:        "max, missed below threshold",
			epochCount:      10,
			epochsToMax:     10,
			missPercentages: []float64{0.1},
			expected:        100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			missPercentagesDecimal := make([]decimal.Decimal, len(tt.missPercentages))
			for i, mp := range tt.missPercentages {
				missPercentagesDecimal[i] = decimal.NewFromFloat(mp)
			}
			result := CalculateReputation(&ReputationContext{
				EpochCount: tt.epochCount,
				ValidationParams: &types.ValidationParams{
					EpochsToMax:          tt.epochsToMax,
					MissPercentageCutoff: 0.1,
				},
				EpochMissPercentages: missPercentagesDecimal,
			})
			require.Equal(t, tt.expected, result)
		})
	}
}
