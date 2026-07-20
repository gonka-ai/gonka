package types

// GetEpochStages calculates and returns the block heights for all
// significant epoch boundaries and exchange windows for the current
// EpochContext. It purposefully does **not** alter any existing logic
// – all offsets are obtained via the already-defined helper methods on
// EpochParams, so changes to the underlying maths automatically flow
// through.
func (ec *EpochContext) GetEpochStages() EpochStages {
	return EpochStages{
		EpochIndex:                 ec.EpochIndex,
		PocStart:                   ec.StartOfPoC(),
		PocGenerationWindDown:      ec.PoCGenerationWindDown(),
		PocGenerationEnd:           ec.EndOfPoCGeneration(),
		PocValidationStart:         ec.StartOfPoCValidation(),
		PocValidationWindDown:      ec.PoCValidationWindDown(),
		PocValidationEnd:           ec.EndOfPoCValidation(),
		SetNewValidators:           ec.SetNewValidators(),
		ClaimMoney:                 ec.ClaimMoney(),
		InferenceValidationCutoff:  ec.InferenceValidationCutoff(),
		NextPocStart:               ec.NextPoCStart(),
		PocExchangeWindow:          ec.PoCExchangeWindow(),
		PocValidationExchangeWindow: ec.ValidationExchangeWindow(),
	}
}
