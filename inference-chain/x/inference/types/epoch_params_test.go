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
