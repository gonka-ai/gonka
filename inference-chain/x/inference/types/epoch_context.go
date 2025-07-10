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
	nextEc := NewEpochContext(
		Epoch{
			Index:               epoch.Index + 1,
			PocStartBlockHeight: ec.NextPoCStart(),
		},
		epochParams,
	)
	if currentBlockHeight < ec.NextPoCStart() &&
		currentBlockHeight > ec.SetNewValidators() {
		return &EpochContext{
			EpochIndex:          epoch.Index,
			PocStartBlockHeight: epoch.PocStartBlockHeight,
			EpochParams:         epochParams,
		}
	} else if currentBlockHeight <= nextEc.SetNewValidators() {
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

// TODO: rework when you implement setting new EpochParams at next epoch?
func (ec *EpochContext) NextEpochContext() EpochContext {
	return EpochContext{
		EpochIndex:          ec.EpochIndex + 1,
		PocStartBlockHeight: ec.NextPoCStart(),
		EpochParams:         ec.EpochParams,
	}
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
		// InferencePhase is the safest default in case of a state mismatch like this
		return InferencePhase
	}

	startOfPoC := ec.StartOfPoC()
	pocGenerateWindDownStart := ec.PoCGenerationWindDown()
	startOfPoCValidation := ec.StartOfPoCValidation()
	pocValidateWindDownStart := ec.PoCValidationWindDown()
	endOfPoCValidation := ec.EndOfPoCValidation()

	if blockHeight >= startOfPoC && blockHeight < pocGenerateWindDownStart {
		return PoCGeneratePhase
	}
	if blockHeight >= pocGenerateWindDownStart && blockHeight < startOfPoCValidation {
		return PoCGenerateWindDownPhase
	}
	if blockHeight >= startOfPoCValidation && blockHeight < pocValidateWindDownStart {
		return PoCValidatePhase
	}
	if blockHeight >= pocValidateWindDownStart && blockHeight < endOfPoCValidation {
		return PoCValidateWindDownPhase
	}

	return InferencePhase
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
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.PocStartBlockHeight // alias for readability
}

func (ec *EpochContext) PoCGenerationWindDown() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetPoCWindDownStage()
}

func (ec *EpochContext) EndOfPoCGeneration() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetEndOfPoCStage()
}

func (ec *EpochContext) PoCExchangeDeadline() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetPoCExchangeDeadline()
}

func (ec *EpochContext) StartOfPoCValidation() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetStartOfPoCValidationStage()
}

func (ec *EpochContext) PoCValidationWindDown() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetPoCValidationWindDownStage()
}

func (ec *EpochContext) EndOfPoCValidation() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetEndOfPoCValidationStage()
}

func (ec *EpochContext) SetNewValidators() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetSetNewValidatorsStage()
}

func (ec *EpochContext) ClaimMoney() int64 {
	if ec.EpochIndex == 0 {
		return 0
	}
	return ec.getPocAnchor() + ec.EpochParams.GetClaimMoneyStage()
}

func (ec *EpochContext) NextPoCStart() int64 {
	if ec.EpochIndex == 0 {
		return -ec.EpochParams.EpochShift + ec.EpochParams.EpochLength
	}
	return ec.PocStartBlockHeight + ec.EpochParams.EpochLength
}

// --- Exchange windows -------------------------------------------------------

func (ec *EpochContext) PoCExchangeWindow() EpochExchangeWindow {
	if ec.EpochIndex == 0 {
		return EpochExchangeWindow{Start: 0, End: 0} // no PoC in epoch 0
	}

	return EpochExchangeWindow{
		Start: ec.StartOfPoC() + 1, // window opens one block *after* PoC start
		End:   ec.PoCExchangeDeadline(),
	}
}

func (ec *EpochContext) ValidationExchangeWindow() EpochExchangeWindow {
	if ec.EpochIndex == 0 {
		return EpochExchangeWindow{Start: 0, End: 0} // no PoC validation in epoch 0
	}

	return EpochExchangeWindow{
		Start: ec.StartOfPoCValidation() + 1, // closed interval [start, end]
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
	return blockHeight == ec.EndOfPoCGeneration()
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
