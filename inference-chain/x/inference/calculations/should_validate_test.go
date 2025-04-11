package calculations

import (
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
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
