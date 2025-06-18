package types_test

import (
	"github.com/productscience/inference/x/inference/types"
	"testing"
)

func TestEpochParamsStages(t *testing.T) {
	// Initialize parameters.
	params := types.EpochParams{
		EpochLength:     2000,
		EpochShift:      1990,
		EpochMultiplier: 2,
	}

	pocStart := int64(10)
	if !params.IsStartOfPoCStage(pocStart) {
		t.Errorf("Expected %d to be the start of PoC stage", pocStart)
	}

	pocEnd := pocStart + params.GetEndOfPoCStage()
	if !params.IsEndOfPoCStage(pocEnd) {
		t.Errorf("Expected %d to be the end of PoC stage", pocEnd)
	}
	if pocEnd != pocStart+params.EpochMultiplier*params.PocExchangeDuration {
		t.Errorf("Expected %d to be the end of PoC stage", pocEnd)
	}

	for i := pocStart + 1; i <= pocStart+params.GetPoCExchangeDeadline(); i++ {
		if !params.IsPoCExchangeWindow(pocStart, i) {
			t.Errorf("Expected block %d to be in PoC exchange window", i)
		}
	}

	if params.IsPoCExchangeWindow(pocStart, pocStart) {
		t.Errorf("Expected block %d not to be in PoC exchange window (zero elapsed epochs)", pocStart)
	}

	if params.IsPoCExchangeWindow(pocStart, pocStart+params.GetPoCExchangeDeadline()+1) {
		t.Errorf("Expected block %d not to be in PoC exchange window (beyond deadline)", pocStart+params.GetPoCExchangeDeadline()+1)
	}

	pocValStart := pocStart + params.GetStartOfPoCValidationStage()
	if pocValStart != pocEnd+(params.PocExchangeDuration*params.EpochMultiplier) {
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

	setNewValidatorsBlock := pocStart + params.GetSetNewValidatorsStage()
	if !params.IsSetNewValidatorsStage(setNewValidatorsBlock) {
		t.Errorf("Expected %d to be the Set New Validators stage", setNewValidatorsBlock)
	}

	startFromPocEnd := params.GetStartBlockHeightFromEndOfPocStage(pocEnd)
	if startFromPocEnd != pocStart {
		t.Errorf("Expected start block height from end of PoC stage to be %d, got %d", pocStart, startFromPocEnd)
	}

	startFromPocValidation := params.GetStartBlockHeightFromStartOfPocValidationStage(pocValStart)
	if startFromPocValidation != pocStart {
		t.Errorf("Expected start block height from start of PoC validation stage to be %d, got %d", pocStart, startFromPocValidation)
	}
}
