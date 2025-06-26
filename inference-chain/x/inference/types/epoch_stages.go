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
	EpochIndex            uint64              `json:"epoch_index"`
	PocStart              int64               `json:"poc_start"`
	PocGenerationWinddown int64               `json:"poc_generation_winddown"`
	PocGenerationEnd      int64               `json:"poc_generation_end"`
	PocValidationStart    int64               `json:"poc_validation_start"`
	PocValidationWinddown int64               `json:"poc_validation_winddown"`
	PocValidationEnd      int64               `json:"poc_validation_end"`
	SetNewValidators      int64               `json:"set_new_validators"`
	ClaimMoney            int64               `json:"claim_money"`
	NextPocStart          int64               `json:"next_poc_start"`
	PocExchangeWindow     EpochExchangeWindow `json:"poc_exchange_window"`
	PocValExchangeWindow  EpochExchangeWindow `json:"poc_validation_exchange_window"`
}

// GetEpochStages calculates and returns the block heights for all
// significant epoch boundaries and exchange windows for the current
// EpochContext. It purposefully does **not** alter any existing logic
// – all offsets are obtained via the already-defined helper methods on
// EpochParams, so changes to the underlying maths automatically flow
// through.
func (ec *EpochContext) GetEpochStages() EpochStages {
	// Absolute anchors – reused multiple times for readability.
	base := ec.PocStartBlockHeight
	params := &ec.EpochParams

	// Helper to avoid repeating conversions.
	rel := func(offset int64) int64 { return base + offset }

	stages := EpochStages{
		EpochIndex:            ec.EpochIndex,
		PocStart:              base,
		PocGenerationWinddown: rel(params.GetPoCWinddownStage()),
		PocGenerationEnd:      rel(params.GetEndOfPoCStage()),
		PocValidationStart:    rel(params.GetStartOfPoCValidationStage()),
		PocValidationWinddown: rel(params.GetPoCValidationWindownStage()),
		PocValidationEnd:      rel(params.GetEndOfPoCValidationStage()),
		SetNewValidators:      rel(params.GetSetNewValidatorsStage()),
		ClaimMoney:            rel(params.GetClaimMoneyStage()),
		NextPocStart:          base + params.EpochLength,
	}

	// PoC exchange window: (PocStart, PocExchangeDeadline] – note the
	// window opens one block **after** PocStart to match
	// IsPoCExchangeWindow logic (>0).
	stages.PocExchangeWindow = EpochExchangeWindow{
		Start: base + 1,
		End:   rel(params.GetPoCExchangeDeadline()),
	}

	// Validation exchange window: (PoCValidationStart, SetNewValidators]
	stages.PocValExchangeWindow = EpochExchangeWindow{
		Start: rel(params.GetStartOfPoCValidationStage()) + 1,
		End:   rel(params.GetSetNewValidatorsStage()),
	}

	return stages
}
