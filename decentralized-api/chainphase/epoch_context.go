package chainphase

import "github.com/productscience/inference/x/inference/types"

// EpochContext provides a stable context for an epoch, anchored by its starting block height.
// It is used to reliably calculate phases and transitions regardless of changes to epoch parameters.
type EpochContext struct {
	pocStartBlockHeight uint64
	epochParams         types.EpochParams
}

// NewEpochContext creates a new, stable context for epoch calculations.
func NewEpochContext(epochGroup *types.EpochGroupData, epochParams types.EpochParams) *EpochContext {
	return &EpochContext{
		pocStartBlockHeight: epochGroup.PocStartBlockHeight,
		epochParams:         epochParams,
	}
}

// GetCurrentPhase calculates the current epoch phase based on the block height relative to the epoch's start.
func (ec *EpochContext) GetCurrentPhase(blockHeight int64) types.EpochPhase {
	// Use the reliable PocStartBlockHeight as the anchor for all calculations.
	epochStartHeight := ec.pocStartBlockHeight
	if blockHeight < int64(epochStartHeight) {
		// This can happen during the initial epoch or if there's a state mismatch.
		// InferencePhase is the safest default.
		return types.InferencePhase
	}

	// Calculate height relative to the epoch's true start.
	relativeBlockHeight := blockHeight - int64(epochStartHeight)

	// If we are past the current epoch's length, we are in the PoC stages for the *next* epoch,
	// but the "phase" is determined by the offsets defined in the *current* epoch's parameters.
	relativeBlockHeight = relativeBlockHeight % ec.epochParams.EpochLength

	startOfPoC := ec.epochParams.GetStartOfPoCStage()
	endOfPoC := ec.epochParams.GetEndOfPoCStage()
	startOfPoCValidation := ec.epochParams.GetStartOfPoCValidationStage()
	endOfPoCValidation := ec.epochParams.GetEndOfPoCValidationStage()

	pocGenerateDuration := endOfPoC - startOfPoC
	pocGenerateWindDownStart := startOfPoC + int64(float64(pocGenerateDuration)*types.PoCGenerateWindDownFactor)

	pocValidateDuration := endOfPoCValidation - startOfPoCValidation
	pocValidateWindDownStart := startOfPoCValidation + int64(float64(pocValidateDuration)*types.PoCValidateWindDownFactor)

	if relativeBlockHeight >= startOfPoC && relativeBlockHeight < pocGenerateWindDownStart {
		return types.PoCGeneratePhase
	}
	if relativeBlockHeight >= pocGenerateWindDownStart && relativeBlockHeight < startOfPoCValidation {
		return types.PoCGenerateWindDownPhase
	}
	if relativeBlockHeight >= startOfPoCValidation && relativeBlockHeight < pocValidateWindDownStart {
		return types.PoCValidatePhase
	}
	if relativeBlockHeight >= pocValidateWindDownStart && relativeBlockHeight < endOfPoCValidation {
		return types.PoCValidateWindDownPhase
	}

	return types.InferencePhase
}

// isAtPhaseBoundary checks if the given block height is at a specific phase boundary within the epoch.
func (ec *EpochContext) isAtPhaseBoundary(blockHeight, phaseOffset int64) bool {
	if ec.IsStartOfNextPoC(blockHeight) {
		return phaseOffset == ec.epochParams.GetStartOfPoCStage()
	}

	relativeHeight := blockHeight - int64(ec.pocStartBlockHeight)
	if relativeHeight < 0 {
		return false
	}

	return relativeHeight%ec.epochParams.EpochLength == phaseOffset
}

// IsStartOfNextPoC determines if the given block height triggers the start of the PoC for the next epoch.
func (ec *EpochContext) IsStartOfNextPoC(blockHeight int64) bool {
	return blockHeight == int64(ec.pocStartBlockHeight)+ec.epochParams.EpochLength
}

func (ec *EpochContext) IsEndOfPoCStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.epochParams.GetEndOfPoCStage())
}

func (ec *EpochContext) IsStartOfPoCValidationStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.epochParams.GetStartOfPoCValidationStage())
}

func (ec *EpochContext) IsEndOfPoCValidationStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.epochParams.GetEndOfPoCValidationStage())
}

func (ec *EpochContext) IsSetNewValidatorsStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.epochParams.GetSetNewValidatorsStage())
}

func (ec *EpochContext) IsClaimMoneyStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.epochParams.GetClaimMoneyStage())
}
