package types

import (
	"fmt"
)

// EpochContext provides a stable context for an Epoch, anchored by its starting block height.
// It is used to reliably calculate phases and transitions regardless of changes to Epoch parameters.
type EpochContext struct {
	EpochIndex          uint64
	PocStartBlockHeight int64
	EpochParams         EpochParams
}

func NewEpochContext(epoch Epoch, params EpochParams) EpochContext {
	return EpochContext{
		EpochIndex:          epoch.Index,
		PocStartBlockHeight: epoch.PocStartBlockHeight,
		EpochParams:         params,
	}
}

// NewEpochContextFromEffectiveEpoch determines the most up-to-date Epoch context based on the current block height.
func NewEpochContextFromEffectiveEpoch(epoch Epoch, epochParams EpochParams, currentBlockHeight int64) *EpochContext {
	if currentBlockHeight < getNextPoCStart(epoch, &epochParams) &&
		currentBlockHeight > getGroupBecomesEffective(epoch, &epochParams) {
		return &EpochContext{
			EpochIndex:          epoch.Index,
			PocStartBlockHeight: epoch.PocStartBlockHeight,
			EpochParams:         epochParams,
		}
	} else if currentBlockHeight <= getNextGroupBecomesEffective(epoch, &epochParams) {
		return &EpochContext{
			EpochIndex:          epoch.Index + 1,
			PocStartBlockHeight: getNextPoCStart(epoch, &epochParams),
			EpochParams:         epochParams,
		}
	} else {
		// This is a special case where the current block height is beyond the expected range.
		// It should not happen in normal operation, but we handle it gracefully.
		msg := fmt.Sprintf("Unexpected currentBlockHeight = %d for epoch.PocStartBlockHeight = %d",
			currentBlockHeight,
			epoch.PocStartBlockHeight)
		panic(msg)
	}
}

func getNextPoCStart(epoch Epoch, params *EpochParams) int64 {
	if params == nil {
		panic("getNextPoCStart: params cannot be nil")
	}

	if epoch.Index == 0 {
		return -params.EpochShift + params.EpochLength
	}

	return epoch.PocStartBlockHeight + params.EpochLength
}

func getGroupBecomesEffective(epoch Epoch, epochParams *EpochParams) int64 {
	if epochParams == nil {
		panic("getGroupBecomesEffective: epochParams cannot be nil")
	}

	if epoch.Index == 0 {
		return 0
	}

	return epoch.PocStartBlockHeight + epochParams.GetSetNewValidatorsStage()
}

func getNextGroupBecomesEffective(epoch Epoch, params *EpochParams) int64 {
	if params == nil {
		panic("getNextGroupBecomesEffective: params cannot be nil")
	}

	if epoch.Index == 0 {
		return -params.EpochShift + params.EpochLength + params.GetSetNewValidatorsStage()
	}

	return epoch.PocStartBlockHeight + params.EpochLength + params.GetSetNewValidatorsStage()
}

// GetCurrentPhase calculates the current Epoch phase based on the block height relative to the Epoch's start.
func (ec *EpochContext) GetCurrentPhase(blockHeight int64) EpochPhase {
	// We don't do PoC for epoch 0, so we return InferencePhase.
	if ec.EpochIndex == 0 {
		return InferencePhase
	}

	// Use the reliable PocStartBlockHeight as the anchor for all calculations.
	epochStartHeight := ec.PocStartBlockHeight
	if blockHeight < epochStartHeight {
		// This can happen during the initial Epoch or if there's a state mismatch.
		// InferencePhase is the safest default.
		return InferencePhase
	}

	relativeBlockHeight := ec.getRelativeBlockHeight(blockHeight)

	startOfPoC := ec.EpochParams.getStartOfPocStage()
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

func (ec *EpochContext) getRelativeBlockHeight(blockHeight int64) int64 {
	if blockHeight < ec.PocStartBlockHeight {
		// TODO: LOG SMTH HERE IF block height is out of bounds?
		return -1
	}

	if ec.PocStartBlockHeight == 0 {
		return blockHeight + ec.EpochParams.EpochShift - ec.PocStartBlockHeight
	} else {
		return blockHeight - ec.PocStartBlockHeight
	}
}

// isAtPhaseBoundary checks if the given block height is at a specific phase boundary within the Epoch.
func (ec *EpochContext) isAtPhaseBoundary(blockHeight, phaseOffset int64) bool {
	// We don't do PoC for epoch 0, so we return false.
	if ec.EpochIndex == 0 {
		return false
	}

	if ec.IsStartOfNextPoC(blockHeight) {
		return phaseOffset == ec.EpochParams.getStartOfPocStage()
	}

	relativeBlockHeight := ec.getRelativeBlockHeight(blockHeight)
	if relativeBlockHeight < 0 {
		return false
	}

	return relativeBlockHeight == phaseOffset
}

// TODO: inspect this function usage!!
// IsStartOfNextPoC determines if the given block height triggers the start of the PoC for the next Epoch.
func (ec *EpochContext) IsStartOfNextPoC(blockHeight int64) bool {
	return blockHeight == ec.PocStartBlockHeight+ec.EpochParams.EpochLength
}

func (ec *EpochContext) IsStartOfPocStage(blockHeight int64) bool {
	return blockHeight == ec.PocStartBlockHeight
}

func (ec *EpochContext) IsPoCExchangeWindow(blockHeight int64) bool {
	relativeBlockHeight := ec.getRelativeBlockHeight(blockHeight)
	if relativeBlockHeight <= 0 {
		return false
	}

	return relativeBlockHeight <= ec.EpochParams.GetPoCExchangeDeadline()
}

func (ec *EpochContext) IsValidationExchangeWindow(blockHeight int64) bool {
	relativeBlockHeight := ec.getRelativeBlockHeight(blockHeight)
	if relativeBlockHeight <= 0 {
		return false
	}

	return relativeBlockHeight > ec.EpochParams.GetStartOfPoCValidationStage() &&
		relativeBlockHeight <= ec.EpochParams.GetSetNewValidatorsStage()
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
