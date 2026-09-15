package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestEpochParamsStages(t *testing.T) {
	// Initialize parameters.
	params := types.EpochParams{
		EpochLength:           2000,
		EpochShift:            1990,
		PocStageDuration:      20,
		PocExchangeDuration:   1,
		PocValidationDelay:    2,
		PocValidationDuration: 10,
	}

	pocStart := int64(10)

	pocEnd := pocStart + params.GetEndOfPoCStage()
	if pocEnd != pocStart+params.PocStageDuration {
		t.Errorf("Expected %d to be the end of PoC stage", pocEnd)
	}

	pocValStart := pocStart + params.GetStartOfPoCValidationStage()
	if pocValStart != pocEnd+params.PocValidationDelay {
		t.Errorf("Expected %d to be the start of PoC Validation stage", pocValStart)
	}
}

func TestEpochParamsValidatePruningLimits(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*types.EpochParams)
		wantError string
	}{
		{
			name: "zero inference limit",
			configure: func(params *types.EpochParams) {
				params.InferencePruningMax = 0
			},
			wantError: "inference pruning max must be positive",
		},
		{
			name: "negative inference limit",
			configure: func(params *types.EpochParams) {
				params.InferencePruningMax = -1
			},
			wantError: "inference pruning max must be positive",
		},
		{
			name: "zero poc limit",
			configure: func(params *types.EpochParams) {
				params.PocPruningMax = 0
			},
			wantError: "poc pruning max must be positive",
		},
		{
			name: "negative poc limit",
			configure: func(params *types.EpochParams) {
				params.PocPruningMax = -1
			},
			wantError: "poc pruning max must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := types.DefaultEpochParams()
			tt.configure(params)
			require.ErrorContains(t, params.Validate(), tt.wantError)
		})
	}
}
