package chainphase

import "github.com/productscience/inference/x/inference/types"

// EpochContext provides a stable context for an Epoch, anchored by its starting block height.
// It is used to reliably calculate phases and transitions regardless of changes to Epoch parameters.
type EpochContext struct {
	Epoch               uint64
	PocStartBlockHeight uint64
	EpochParams         types.EpochParams
}

func NewEpochContext(epochGroup *types.EpochGroupData, epochParams types.EpochParams, currentBlockHeight int64) *EpochContext {
	if currentBlockHeight >= int64(epochGroup.PocStartBlockHeight)+epochParams.EpochLength {
		return &EpochContext{
			Epoch:               epochGroup.EpochGroupId + 1,
			PocStartBlockHeight: epochGroup.PocStartBlockHeight + uint64(epochParams.EpochLength),
			EpochParams:         epochParams,
		}
	} else {
		return &EpochContext{
			Epoch:               epochGroup.EpochGroupId,
			PocStartBlockHeight: epochGroup.PocStartBlockHeight,
			EpochParams:         epochParams,
		}
	}
}

// GetCurrentPhase calculates the current Epoch phase based on the block height relative to the Epoch's start.
func (ec *EpochContext) GetCurrentPhase(blockHeight int64) types.EpochPhase {
	// Use the reliable PocStartBlockHeight as the anchor for all calculations.
	epochStartHeight := ec.PocStartBlockHeight
	if blockHeight < int64(epochStartHeight) {
		// This can happen during the initial Epoch or if there's a state mismatch.
		// InferencePhase is the safest default.
		return types.InferencePhase
	}

	// Calculate height relative to the Epoch's true start.
	relativeBlockHeight := blockHeight - int64(epochStartHeight)

	startOfPoC := ec.EpochParams.GetStartOfPoCStage()
	endOfPoC := ec.EpochParams.GetEndOfPoCStage()
	startOfPoCValidation := ec.EpochParams.GetStartOfPoCValidationStage()
	endOfPoCValidation := ec.EpochParams.GetEndOfPoCValidationStage()

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

// isAtPhaseBoundary checks if the given block height is at a specific phase boundary within the Epoch.
func (ec *EpochContext) isAtPhaseBoundary(blockHeight, phaseOffset int64) bool {
	if ec.IsStartOfNextPoC(blockHeight) {
		return phaseOffset == ec.EpochParams.GetStartOfPoCStage()
	}

	relativeHeight := blockHeight - int64(ec.PocStartBlockHeight)
	if relativeHeight < 0 {
		return false
	}

	return relativeHeight%ec.EpochParams.EpochLength == phaseOffset
}

// IsStartOfNextPoC determines if the given block height triggers the start of the PoC for the next Epoch.
func (ec *EpochContext) IsStartOfNextPoC(blockHeight int64) bool {
	return blockHeight == int64(ec.PocStartBlockHeight)+ec.EpochParams.EpochLength
}

func (ec *EpochContext) IsEndOfPoCStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.EpochParams.GetEndOfPoCStage())
}

func (ec *EpochContext) IsStartOfPoCValidationStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.EpochParams.GetStartOfPoCValidationStage())
}

func (ec *EpochContext) IsEndOfPoCValidationStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.EpochParams.GetEndOfPoCValidationStage())
}

func (ec *EpochContext) IsSetNewValidatorsStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.EpochParams.GetSetNewValidatorsStage())
}

func (ec *EpochContext) IsClaimMoneyStage(blockHeight int64) bool {
	return ec.isAtPhaseBoundary(blockHeight, ec.EpochParams.GetClaimMoneyStage())
}
