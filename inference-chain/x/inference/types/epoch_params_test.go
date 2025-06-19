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
	if !params.IsEndOfPoCStage(pocEnd) {
		t.Errorf("Expected %d to be the end of PoC stage", pocEnd)
	}
	if pocEnd != pocStart+params.PocStageDuration {
		t.Errorf("Expected %d to be the end of PoC stage", pocEnd)
	}

	pocValStart := pocStart + params.GetStartOfPoCValidationStage()
	if pocValStart != pocEnd+params.PocValidationDelay {
		t.Errorf("Expected %d to be the start of PoC Validation stage", pocValStart)
	}
	if !params.IsStartOfPoCValidationStage(pocValStart) {
		t.Errorf("Expected %d to be the start of PoC Validation stage", pocValStart)
	}

	for i := pocValStart + 1; i <= pocValStart+params.GetSetNewValidatorsStage(); i++ {
		if !params.IsValidationExchangeWindow(pocValStart, i) {
			t.Errorf("Expected block %d to be in Validation exchange window", i)
		}
	}

	if params.IsValidationExchangeWindow(pocValStart, pocValStart) {
		t.Errorf("Expected block %d not to be in Validation exchange window (zero elapsed epochs)", pocValStart)
	}

	if params.IsValidationExchangeWindow(pocValStart, pocValStart+params.GetSetNewValidatorsStage()+1) {
		t.Errorf("Expected block %d not to be in Validation exchange window (beyond deadline)", pocValStart+params.GetSetNewValidatorsStage()+1)
	}

	pocValEnd := pocStart + params.GetEndOfPoCValidationStage()
	if !params.IsEndOfPoCValidationStage(pocValEnd) {
		t.Errorf("Expected %d to be the end of PoC Validation stage", pocValEnd)
	}
}
