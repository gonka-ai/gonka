package calculations

import (
	"fmt"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const (
	performanceTestIterations int64 = 1_000_000
)

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
				ExecutorReputation: 0,
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
				ExecutorReputation: 100,
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
				ExecutorReputation: 50,
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
				ExecutorReputation: 100,
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
				ExecutorReputation: 100,
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
				ExecutorReputation: 0,
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
				ExecutorReputation: 0,
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
				ExecutorReputation: 100,
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
				ExecutorReputation: 0,
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
				ExecutorReputation: 100,
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
				ExecutorReputation: 100,
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
				MinValidationAverage:        types.DecimalFromFloat(tt.minValidationAverage),
				MaxValidationAverage:        types.DecimalFromFloat(tt.maxValidationAverage),
				FullValidationTrafficCutoff: defaultTrafficCutoff,
				MinValidationTrafficCutoff:  100,
				MinValidationHalfway:        types.DecimalFromFloat(0.05),
				EpochsToMax:                 defaultEpochsToMax,
			}
			_ = testParams
			result, text := ShouldValidate(tt.seed, tt.inferenceDetails, tt.totalPower, tt.validatorPower, tt.executorPower, testParams, true)
			t.Logf("ValidationDecision: %s", text)
			_, _, ourProbability, err := ExtractValidationDetails(text)
			require.NoError(t, err)

			require.InEpsilon(t, tt.expectedProbability, ourProbability, 0.01,
				fmt.Sprintf("Expected probability %f but got %f", tt.expectedProbability, ourProbability))
			require.Equal(t, tt.expectedResult, result)
		})
	}

}

func TestShouldValidate_DivisionByZeroGuard(t *testing.T) {
	details := &types.InferenceValidationDetails{InferenceId: fixedInferenceId, TrafficBasis: defaultTrafficCutoff}
	params := &types.ValidationParams{
		MinValidationAverage: types.DecimalFromFloat(0.1), MaxValidationAverage: types.DecimalFromFloat(1.0),
		FullValidationTrafficCutoff: defaultTrafficCutoff, MinValidationTrafficCutoff: 100,
		MinValidationHalfway: types.DecimalFromFloat(0.05), EpochsToMax: defaultEpochsToMax,
	}

	result, _ := ShouldValidate(0, details, 100, 50, 100, params, false)
	require.False(t, result)

	result, _ = ShouldValidate(0, details, 50, 25, 100, params, false)
	require.False(t, result)
}

func TestShouldValidatePerformance(t *testing.T) {
	inferenceDetails := &types.InferenceValidationDetails{
		InferenceId:        fixedInferenceId,
		TrafficBasis:       defaultTrafficCutoff + 1,
		ExecutorReputation: 50,
	}

	testParams := &types.ValidationParams{
		MinValidationAverage:        types.DecimalFromFloat(0.1),
		MaxValidationAverage:        types.DecimalFromFloat(1.0),
		FullValidationTrafficCutoff: defaultTrafficCutoff,
		MinValidationTrafficCutoff:  100,
		MinValidationHalfway:        types.DecimalFromFloat(0.05),
		EpochsToMax:                 defaultEpochsToMax,
	}

	start := time.Now()

	for i := int64(0); i < performanceTestIterations; i++ {
		ShouldValidate(i, inferenceDetails, 300, 100, 50, testParams, false)
	}

	elapsed := time.Since(start)
	averageTimePerValidation := float64(elapsed.Nanoseconds()) / float64(performanceTestIterations)

	t.Logf("Performed %d validations in %v (average %.2f ns per validation)",
		performanceTestIterations, elapsed, averageTimePerValidation)
}

func TestDeterministicFloat(t *testing.T) {
	tests := []struct {
		seed       int64
		identifier string
		expected   string
	}{
		{
			seed:       12345,
			identifier: "inference-1",
			expected:   "0.5498462437774096",
		},
		{
			seed:       67890,
			identifier: "inference-2",
			expected:   "0.1626630352875919",
		},
		{
			seed:       0,
			identifier: "inference-3",
			expected:   "0.8815494969577052",
		},
		{
			seed:       -12345,
			identifier: "inference-4",
			expected:   "0.0456115115524204",
		},
		{
			seed:       999999999,
			identifier: "very-long-inference-identifier-string-just-to-be-sure",
			expected:   "0.9774181254878734",
		},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("seed_%d_id_%s", tt.seed, tt.identifier), func(t *testing.T) {
			result := DeterministicFloat(tt.seed, tt.identifier)
			require.Equal(t, tt.expected, result.String(), "DeterministicFloat result changed!")
		})
	}
}
