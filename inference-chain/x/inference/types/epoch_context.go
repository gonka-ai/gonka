package types

import (
	"fmt"
)

// EpochContext provides a stable context for an Epoch, anchored by its starting block height.
// It is used to reliably calculate phases and transitions regardless of changes to Epoch parameters.
type EpochContext struct {
	Epoch               uint64
	PocStartBlockHeight int64
	EpochParams         EpochParams
}

func NewEpochContext(epochGroup *EpochGroupData, epochParams EpochParams, currentBlockHeight int64) *EpochContext {
	if epochGroup == nil {
		if currentBlockHeight < epochParams.EpochLength-epochParams.EpochShift {
			return &EpochContext{
				Epoch: 0,
				// FIXME: or maybe make it zero and return to uint64 type here to avoid confusion?
				PocStartBlockHeight: -epochParams.EpochShift,
				EpochParams:         epochParams,
			}
		} else if currentBlockHeight <= (epochParams.EpochLength-epochParams.EpochShift)+epochParams.GetSetNewValidatorsStage() {
			return &EpochContext{
				Epoch:               1,
				PocStartBlockHeight: epochParams.EpochLength - epochParams.EpochShift,
				EpochParams:         epochParams,
			}
		} else {
			msg := fmt.Sprintf("epoch group in nil. currentBlockHeight = %d", currentBlockHeight)
			panic(msg)
		}
	}

	// TODO: should probably check if currentBlockHeight is still in the boundaries of the next epoch!
	if currentBlockHeight >= int64(epochGroup.PocStartBlockHeight)+epochParams.EpochLength {
		return &EpochContext{
			Epoch:               epochGroup.EpochGroupId + 1,
			PocStartBlockHeight: int64(epochGroup.PocStartBlockHeight) + epochParams.EpochLength,
			EpochParams:         epochParams,
		}
	} else {
		return &EpochContext{
			Epoch:               epochGroup.EpochGroupId,
			PocStartBlockHeight: int64(epochGroup.PocStartBlockHeight),
			EpochParams:         epochParams,
		}
	}
}

// GetCurrentPhase calculates the current Epoch phase based on the block height relative to the Epoch's start.
func (ec *EpochContext) GetCurrentPhase(blockHeight int64) EpochPhase {
	// Use the reliable PocStartBlockHeight as the anchor for all calculations.
	epochStartHeight := ec.PocStartBlockHeight
	if blockHeight < epochStartHeight {
		// This can happen during the initial Epoch or if there's a state mismatch.
		// InferencePhase is the safest default.
		return InferencePhase
	}

	// Calculate height relative to the Epoch's true start.
	relativeBlockHeight := blockHeight - epochStartHeight

	startOfPoC := ec.EpochParams.GetStartOfPoCStage()
	pocGenerateWindDownStart := ec.EpochParams.GetPoCWinddownStage()
	startOfPoCValidation := ec.EpochParams.GetStartOfPoCValidationStage()
	pocValidateWindDownStart := ec.EpochParams.GetPoCValidationWindownStage()
	endOfPoCValidation := ec.EpochParams.GetEndOfPoCValidationStage()

	if relativeBlockHeight >= startOfPoC && relativeBlockHeight < pocGenerateWindDownStart {
		return PoCGeneratePhase
	}
	if relativeBlockHeight >= pocGenerateWindDownStart && relativeBlockHeight < startOfPoCValidation {
		return PoCGenerateWindDownPhase
	}
	if relativeBlockHeight >= startOfPoCValidation && relativeBlockHeight < pocValidateWindDownStart {
		return PoCValidatePhase
	}
	if relativeBlockHeight >= pocValidateWindDownStart && relativeBlockHeight < endOfPoCValidation {
		return PoCValidateWindDownPhase
	}

	return InferencePhase
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
	return blockHeight == ec.PocStartBlockHeight+ec.EpochParams.EpochLength
}

func (ec *EpochContext) IsStartOfPoc(blockHeight int64) bool {
	return blockHeight == ec.PocStartBlockHeight
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
