package types

// ConfirmationPoCEvent helper methods provide calculated heights for a confirmation PoC event.
// Similar to how EpochContext calculates heights from PocStartBlockHeight.
// This creates a unified timing model between regular and confirmation PoC.

func (e *ConfirmationPoCEvent) GetGenerationEnd(params *EpochParams) int64 {
	return e.GenerationStartHeight + params.PocStageDuration - 1
}

func (e *ConfirmationPoCEvent) GetExchangeEnd(params *EpochParams) int64 {
	return e.GetGenerationEnd(params) + params.PocExchangeDuration
}

func (e *ConfirmationPoCEvent) GetValidationStart(params *EpochParams) int64 {
	return e.GetExchangeEnd(params) + params.PocValidationDelay
}

func (e *ConfirmationPoCEvent) GetValidationEnd(params *EpochParams) int64 {
	return e.GetValidationStart(params) + params.PocValidationDuration - 1
}

// Window checks
func (e *ConfirmationPoCEvent) IsInGenerationWindow(blockHeight int64, params *EpochParams) bool {
	return blockHeight >= e.GenerationStartHeight && blockHeight <= e.GetGenerationEnd(params)
}

func (e *ConfirmationPoCEvent) IsInExchangeWindow(blockHeight int64, params *EpochParams) bool {
	generationEnd := e.GetGenerationEnd(params)
	return blockHeight > generationEnd && blockHeight <= e.GetExchangeEnd(params)
}

func (e *ConfirmationPoCEvent) IsInBatchSubmissionWindow(blockHeight int64, params *EpochParams) bool {
	// Batches accepted during generation AND exchange (like regular PoC)
	return blockHeight >= e.GenerationStartHeight && blockHeight <= e.GetExchangeEnd(params)
}

func (e *ConfirmationPoCEvent) IsInValidationWindow(blockHeight int64, params *EpochParams) bool {
	return blockHeight >= e.GetValidationStart(params) && blockHeight <= e.GetValidationEnd(params)
}

// Phase transition helpers - encapsulate all timing logic
func (e *ConfirmationPoCEvent) ShouldTransitionToGeneration(blockHeight int64) bool {
	return e.Phase == ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD &&
		blockHeight >= e.GenerationStartHeight
}

func (e *ConfirmationPoCEvent) ShouldTransitionToValidation(blockHeight int64, params *EpochParams) bool {
	return e.Phase == ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION &&
		blockHeight >= e.GetValidationStart(params)
}

func (e *ConfirmationPoCEvent) ShouldTransitionToCompleted(blockHeight int64, params *EpochParams) bool {
	return e.Phase == ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION &&
		blockHeight > e.GetValidationEnd(params)
}

// Dapi dispatcher triggers - when to send commands
func (e *ConfirmationPoCEvent) ShouldStartGeneration(blockHeight int64) bool {
	return blockHeight == e.GenerationStartHeight
}

func (e *ConfirmationPoCEvent) ShouldInitValidation(blockHeight int64, params *EpochParams) bool {
	return blockHeight == e.GetExchangeEnd(params)
}

func (e *ConfirmationPoCEvent) ShouldStartValidation(blockHeight int64, params *EpochParams) bool {
	return blockHeight == e.GetValidationStart(params)
}

func (e *ConfirmationPoCEvent) ShouldReturnToInference(blockHeight int64, params *EpochParams) bool {
	return blockHeight == e.GetValidationEnd(params)+1
}

// GetExpectedPhase returns what phase the event should be in at given block height
// This allows catching desync issues and simplifies phase management
func (e *ConfirmationPoCEvent) GetExpectedPhase(blockHeight int64, params *EpochParams) ConfirmationPoCPhase {
	if blockHeight < e.GenerationStartHeight {
		return ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD
	}
	if blockHeight < e.GetValidationStart(params) {
		return ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION
	}
	if blockHeight <= e.GetValidationEnd(params) {
		return ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION
	}
	return ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED
}
