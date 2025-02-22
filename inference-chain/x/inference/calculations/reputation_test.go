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
		testName    string
		epochCount  int64
		epochsToMax int64
		expected    decimal.Decimal
	}{
		{
			testName:    "no epochs",
			epochCount:  0,
			epochsToMax: 30,
			expected:    decimal.NewFromFloat(0.0),
		},
		{
			testName:    "halfway",
			epochCount:  15,
			epochsToMax: 30,
			expected:    decimal.NewFromFloat(0.5),
		},
		{
			testName:    "max",
			epochCount:  30,
			epochsToMax: 30,
			expected:    decimal.NewFromFloat(1.0),
		},
		{
			testName:    "one third (trunc to 2 decimal places)",
			epochCount:  10,
			epochsToMax: 30,
			expected:    decimal.NewFromFloat(0.33),
		},
		{
			testName:    "two thirds (trunc to 2 decimal places)",
			epochCount:  20,
			epochsToMax: 30,
			expected:    decimal.NewFromFloat(0.66),
		},
	}
	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			result := CalculateReputation(ReputationContext{
				EpochCount: tt.epochCount,
				ValidationParams: &types.ValidationParams{
					EpochsToMax: tt.epochsToMax,
				},
			})
			require.True(t, tt.expected.Equal(result))
		})
	}
}

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
				MinValidationHalfway:        tt.halfwayValidationAverage,
				MinValidationAverage:        tt.minimumValidationAverage,
				MaxValidationAverage:        tt.maxValidationAverage,
				MinValidationTrafficCutoff:  tt.minimumValidationCutoff,
			}
			result := CalculateMinimumValidationAverage(tt.recentRequestCount, testParams)
			require.True(t, tt.expectedAverage.Equal(result))
		})
	}
}

func TestShouldValidate(t *testing.T) {
	tests := []struct {
		name                 string
		seed                 int64
		inferenceDetails     *types.InferenceValidationDetails
		totalPower           uint32
		validatorPower       uint32
		executorPower        uint32
		expectedResult       bool
		expectedProbability  float64
		minValidationAverage float64
		maxValidationAverage float64
	}{
		{
			name: "executor reputation 0, full validator power",
			seed: fiftyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 0.0,
			},
			totalPower:           100,
			validatorPower:       50,
			executorPower:        10,
			expectedResult:       true,
			expectedProbability:  0.5555555555555556,
			minValidationAverage: 0.1,
			maxValidationAverage: 1.0,
		},
		{
			name: "executor reputation 1, low validator power",
			seed: fiftyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 1.0,
			},
			totalPower:           200,
			validatorPower:       30,
			executorPower:        20,
			expectedResult:       false,
			expectedProbability:  0.016666671,
			minValidationAverage: 0.1,
			maxValidationAverage: 1.0,
		},
		{
			name: "executor higher power, mid reputation",
			seed: tenPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 0.5,
			},
			totalPower:           300,
			validatorPower:       100,
			executorPower:        50,
			expectedResult:       true,
			expectedProbability:  0.22000001,
			minValidationAverage: 0.1,
			maxValidationAverage: 1.0,
		},
		{
			name: "executor reputation at max, equal powers",
			seed: fiftyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 1.0,
			},
			totalPower:           150,
			validatorPower:       50,
			executorPower:        50,
			expectedResult:       false,
			expectedProbability:  0.05,
			minValidationAverage: 0.1,
			maxValidationAverage: 1.0,
		},
		{
			name: "max reputation, equal powers, small range",
			seed: fiftyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 1.0,
			},
			totalPower:           100,
			validatorPower:       50,
			executorPower:        50,
			expectedResult:       false,
			expectedProbability:  0.5,
			minValidationAverage: 0.5,
			maxValidationAverage: 1.0,
		},
		{
			name: "min reputation, equal powers, small range",
			seed: ninetyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 0.0,
			},
			totalPower:           150,
			validatorPower:       50,
			executorPower:        50,
			expectedResult:       false,
			expectedProbability:  0.5,
			minValidationAverage: 0.5,
			maxValidationAverage: 1.0,
		},
		{
			name: "only one non-executor, bad reputation",
			seed: ninetyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 0.0,
			},
			totalPower:           100,
			validatorPower:       50,
			executorPower:        50,
			expectedResult:       true,
			expectedProbability:  1.0,
			minValidationAverage: 0.5,
			maxValidationAverage: 1.0,
		},
		{
			name: "only one non-executor, perfect reputation",
			seed: ninetyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 1.0,
			},
			totalPower:           100,
			validatorPower:       50,
			executorPower:        50,
			expectedResult:       false,
			expectedProbability:  0.5,
			minValidationAverage: 0.5,
			maxValidationAverage: 1.0,
		},
		{
			name: "never more than 1.0",
			seed: ninetyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff,
				ExecutorReputation: 0.0,
			},
			totalPower:           100,
			validatorPower:       50,
			executorPower:        50,
			expectedResult:       true,
			expectedProbability:  1.0,
			minValidationAverage: 0.5,
			maxValidationAverage: 100.0,
		},
		{
			name: "minimum traffic, perfect reputation",
			seed: fiftyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       100,
				ExecutorReputation: 1.0,
			},
			totalPower:           100,
			validatorPower:       50,
			executorPower:        10,
			expectedResult:       true,
			expectedProbability:  0.5555555555555556,
			minValidationAverage: 0.1,
			maxValidationAverage: 1.0,
		},
		{
			name: "middle traffic, perfect reputation",
			seed: fiftyPercentSeed,
			inferenceDetails: &types.InferenceValidationDetails{
				InferenceId:        fixedInferenceId,
				TrafficBasis:       defaultTrafficCutoff / 2,
				ExecutorReputation: 1.0,
			},
			totalPower:           150,
			validatorPower:       50,
			executorPower:        50,
			expectedResult:       false,
			expectedProbability:  0.025,
			minValidationAverage: 0.01,
			maxValidationAverage: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testParams := &types.ValidationParams{
				MinValidationAverage:        tt.minValidationAverage,
				MaxValidationAverage:        tt.maxValidationAverage,
				FullValidationTrafficCutoff: defaultTrafficCutoff,
				MinValidationTrafficCutoff:  100,
				MinValidationHalfway:        0.05,
				EpochsToMax:                 defaultEpochsToMax,
			}
			_ = testParams
			result, text := ShouldValidate(tt.seed, tt.inferenceDetails, tt.totalPower, tt.validatorPower, tt.executorPower, testParams)
			t.Logf("ValidationDecision: %s", text)
			_, _, ourProbability, err := ExtractValidationDetails(text)
			require.NoError(t, err)

			require.InEpsilon(t, tt.expectedProbability, ourProbability, 0.01,
				fmt.Sprintf("Expected probability %f but got %f", tt.expectedProbability, ourProbability))
			require.Equal(t, tt.expectedResult, result)
		})
	}
}
