package types_test

import (
	"github.com/productscience/inference/x/inference/types"
	"testing"
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
		params.PocExchangeDuration = 0
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
	params.PocExchangeDuration = 0
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
