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
	ec := NewEpochContext(epoch, epochParams)
	if currentBlockHeight < ec.NextPoCStart() &&
		currentBlockHeight > getGroupBecomesEffective(epoch, &epochParams) {
		return &EpochContext{
			EpochIndex:          epoch.Index,
			PocStartBlockHeight: epoch.PocStartBlockHeight,
			EpochParams:         epochParams,
		}
	} else if currentBlockHeight <= getNextGroupBecomesEffective(epoch, &epochParams) {
		return &EpochContext{
			EpochIndex:          epoch.Index + 1,
			PocStartBlockHeight: ec.NextPoCStart(),
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

func (ec *EpochContext) String() string {
	return fmt.Sprintf("EpochContext{EpochIndex:%d PocStartBlockHeight:%d EpochParams:%s}",
		ec.EpochIndex, ec.PocStartBlockHeight, &ec.EpochParams)
}

// Add absolute-height helpers that transform the relative offsets provided by
// EpochParams into concrete block-heights for this specific epoch. Having them
// centralised means any future change to the maths only happens here.

// getPocAnchor returns the absolute block height considered offset 0 for this
// epochʼs PoC calculations. For every epoch except the genesis one this is
// simply PocStartBlockHeight. The genesis epoch does **not** run PoC, so these
// helpers are never used there – but we still return a sensible value.
func (ec *EpochContext) getPocAnchor() int64 {
	// For epoch 0 we keep the anchor at the recorded block height (usually 0).
	return ec.PocStartBlockHeight
}

// --- Absolute boundaries ----------------------------------------------------

func (ec *EpochContext) StartOfPoC() int64 {
	return ec.PocStartBlockHeight // alias for readability
}

func (ec *EpochContext) PoCGenerationWinddown() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetPoCWinddownStage()
}

func (ec *EpochContext) EndOfPoC() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetEndOfPoCStage()
}

func (ec *EpochContext) PoCExchangeDeadline() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetPoCExchangeDeadline()
}

func (ec *EpochContext) StartOfPoCValidation() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetStartOfPoCValidationStage()
}

func (ec *EpochContext) PoCValidationWinddown() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetPoCValidationWindownStage()
}

func (ec *EpochContext) EndOfPoCValidation() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetEndOfPoCValidationStage()
}

func (ec *EpochContext) SetNewValidators() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetSetNewValidatorsStage()
}

func (ec *EpochContext) ClaimMoney() int64 {
	return ec.getPocAnchor() + ec.EpochParams.GetClaimMoneyStage()
}

func (ec *EpochContext) NextPoCStart() int64 {
	if ec.EpochIndex == 0 {
		// For epoch 0 we return the PoC start height as defined in EpochParams.
		return -ec.EpochParams.EpochShift + ec.EpochParams.EpochLength
	}
	return ec.PocStartBlockHeight + ec.EpochParams.EpochLength
}

// --- Exchange windows -------------------------------------------------------

func (ec *EpochContext) PoCExchangeWindow() EpochExchangeWindow {
	return EpochExchangeWindow{
		Start: ec.StartOfPoC() + 1, // window opens one block *after* PoC start
		End:   ec.PoCExchangeDeadline(),
	}
}

func (ec *EpochContext) ValidationExchangeWindow() EpochExchangeWindow {
	return EpochExchangeWindow{
		Start: ec.StartOfPoCValidation() + 1, // open interval (start, end]
		End:   ec.SetNewValidators(),
	}
}

// --- Boolean helpers ---------------

func (ec *EpochContext) IsStartOfPocStage(blockHeight int64) bool {
	return blockHeight == ec.StartOfPoC()
}

func (ec *EpochContext) IsPoCExchangeWindow(blockHeight int64) bool {
	if ec.EpochIndex == 0 {
		return false
	}
	w := ec.PoCExchangeWindow()
	return blockHeight >= w.Start && blockHeight <= w.End
}

func (ec *EpochContext) IsValidationExchangeWindow(blockHeight int64) bool {
	if ec.EpochIndex == 0 {
		return false
	}
	w := ec.ValidationExchangeWindow()
	return blockHeight >= w.Start && blockHeight <= w.End
}

func (ec *EpochContext) IsEndOfPoCStage(blockHeight int64) bool {
	if ec.EpochIndex == 0 {
		return false
	}
	return blockHeight == ec.EndOfPoC()
}

func (ec *EpochContext) IsStartOfPoCValidationStage(blockHeight int64) bool {
	if ec.EpochIndex == 0 {
		return false
	}
	return blockHeight == ec.StartOfPoCValidation()
}

func (ec *EpochContext) IsEndOfPoCValidationStage(blockHeight int64) bool {
	if ec.EpochIndex == 0 {
		return false
	}
	return blockHeight == ec.EndOfPoCValidation()
}

func (ec *EpochContext) IsSetNewValidatorsStage(blockHeight int64) bool {
	if ec.EpochIndex == 0 {
		return false
	}
	return blockHeight == ec.SetNewValidators()
}

func (ec *EpochContext) IsClaimMoneyStage(blockHeight int64) bool {
	if ec.EpochIndex == 0 {
		return false
	}
	return blockHeight == ec.ClaimMoney()
}

func (ec *EpochContext) IsNextPoCStart(blockHeight int64) bool {
	return blockHeight == ec.NextPoCStart()
}
