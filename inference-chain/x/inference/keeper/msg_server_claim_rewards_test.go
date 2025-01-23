package keeper

import (
	"fmt"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

var fixedInferenceId = "inferenceId"

// Given fixedInferenceId, these seeds will produce close (slightly higher) to all of these probabilities
var ninetyPercentSeed = int64(5798067479865859744)
var fiftyPercentSeed = int64(6669939700021626378)
var tenPercentSeed = int64(2925341513999858939)

// ExtractValidationDetails parses and extracts values from a message.
func ExtractValidationDetails(msg string) (shouldValidate bool, randFloat float64, ourProbability float64, err error) {
	// Define the layout to match the expected string format
	_, err = fmt.Sscanf(msg, "Should Validate: %t randFloat: %f ourProbability: %f", &shouldValidate, &randFloat, &ourProbability)
	return
}

func TestShouldValidate(t *testing.T) {
	tests := []struct {
		name                 string
		seed                 int64
		inferenceDetails     *types.InferenceDetail
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
				ExecutorReputation: 1,
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
				ExecutorReputation: 1,
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
				ExecutorReputation: 1,
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
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
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
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
			name: "never more than 1.0",
			seed: ninetyPercentSeed,
			inferenceDetails: &types.InferenceDetail{
				InferenceId:        fixedInferenceId,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testParams := &types.ValidationParams{
				MinValidationAverage: tt.minValidationAverage,
				MaxValidationAverage: tt.maxValidationAverage,
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
