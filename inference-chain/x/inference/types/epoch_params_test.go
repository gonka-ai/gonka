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

func TestEpochParamsValidate_PocValidationDelay(t *testing.T) {
	for _, tc := range []struct {
		delay   int64
		wantErr bool
	}{
		{delay: -1, wantErr: true},
		{delay: 0, wantErr: true},
		{delay: 1, wantErr: false},
		{delay: 5, wantErr: false},
	} {
		params := types.DefaultEpochParams()
		params.PocValidationDelay = tc.delay
		err := params.Validate()
		if tc.wantErr && err == nil {
			t.Errorf("delay %d: expected validation error", tc.delay)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("delay %d: unexpected error: %v", tc.delay, err)
		}
	}
}

// A challenge segment inside a confirmation PoC finishes at ExchangeEnd+1
// (keeper.ChallengeFinish), and voters pass once, at ValidationStart
// (ShouldStartValidation), voting only segments with height >= Finish.
// With delay 0 that single pass happens before the segment finishes.
func TestConfirmationPoCValidationStartsAfterChallengeFinish(t *testing.T) {
	params := types.DefaultEpochParams()
	params.PocValidationDelay = 1
	if err := params.Validate(); err != nil {
		t.Fatal(err)
	}
	event := types.ConfirmationPoCEvent{GenerationStartHeight: 100}
	challengeFinish := event.GetExchangeEnd(params) + 1
	if event.GetValidationStart(params) < challengeFinish {
		t.Errorf("validation start %d is before challenge finish %d",
			event.GetValidationStart(params), challengeFinish)
	}
}
