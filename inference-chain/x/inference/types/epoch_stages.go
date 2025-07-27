package types

// EpochExchangeWindow represents an inclusive window of block heights.
// It is JSON-serializable via struct tags.
type EpochExchangeWindow struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// EpochStages contains absolute block heights for all important
// epoch-related boundaries and windows. All fields are annotated with
// JSON tags to ensure they are directly serialisable.
//
// The values are computed once off an EpochContext, so callers can use
// it for logging / debugging / API responses without re-implementing
// the maths scattered around EpochContext.
type EpochStages struct {
	EpochIndex                uint64              `json:"epoch_index"`
	PocStart                  int64               `json:"poc_start"`
	PocGenerationWindDown     int64               `json:"poc_generation_wind_down"`
	PocGenerationEnd          int64               `json:"poc_generation_end"`
	PocValidationStart        int64               `json:"poc_validation_start"`
	PocValidationWindDown     int64               `json:"poc_validation_wind_down"`
	PocValidationEnd          int64               `json:"poc_validation_end"`
	SetNewValidators          int64               `json:"set_new_validators"`
	ClaimMoney                int64               `json:"claim_money"`
	InferenceValidationCutoff int64               `json:"inference_validation_cutoff"`
	NextPocStart              int64               `json:"next_poc_start"`
	PocExchangeWindow         EpochExchangeWindow `json:"poc_exchange_window"`
	PocValExchangeWindow      EpochExchangeWindow `json:"poc_validation_exchange_window"`
}

// GetEpochStages calculates and returns the block heights for all
// significant epoch boundaries and exchange windows for the current
// EpochContext. It purposefully does **not** alter any existing logic
// – all offsets are obtained via the already-defined helper methods on
// EpochParams, so changes to the underlying maths automatically flow
// through.
func (ec *EpochContext) GetEpochStages() EpochStages {
	return EpochStages{
		EpochIndex:                ec.EpochIndex,
		PocStart:                  ec.StartOfPoC(),
		PocGenerationWindDown:     ec.PoCGenerationWindDown(),
		PocGenerationEnd:          ec.EndOfPoCGeneration(),
		PocValidationStart:        ec.StartOfPoCValidation(),
		PocValidationWindDown:     ec.PoCValidationWindDown(),
		PocValidationEnd:          ec.EndOfPoCValidation(),
		SetNewValidators:          ec.SetNewValidators(),
		ClaimMoney:                ec.ClaimMoney(),
		InferenceValidationCutoff: ec.InferenceValidationCutoff(),
		NextPocStart:              ec.NextPoCStart(),
		PocExchangeWindow:         ec.PoCExchangeWindow(),
		PocValExchangeWindow:      ec.ValidationExchangeWindow(),
	}
}
