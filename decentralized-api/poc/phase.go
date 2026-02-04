package poc

import (
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

// ShouldAcceptGeneratedArtifacts returns true if the system should accept
// incoming artifact batches from MLNodes.
func ShouldAcceptGeneratedArtifacts(epochState *chainphase.EpochState) bool {
	if epochState.IsNilOrNotSynced() {
		return false
	}
	// Regular PoC generation
	if epochState.CurrentPhase == types.PoCGeneratePhase {
		return true
	}
	// Confirmation PoC generation during inference phase
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION {
		return true
	}
	return false
}

// ShouldAcceptValidatedArtifacts returns true if the system should accept
// incoming validation results from MLNodes.
func ShouldAcceptValidatedArtifacts(epochState *chainphase.EpochState) bool {
	if epochState.IsNilOrNotSynced() {
		return false
	}
	// Regular PoC validation
	if epochState.CurrentPhase == types.PoCValidatePhase ||
		epochState.CurrentPhase == types.PoCValidateWindDownPhase {
		return true
	}
	// Confirmation PoC validation during inference phase
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		return true
	}
	return false
}

// GetCurrentPocStageHeight returns the PoC stage start height.
// For regular PoC: PocStartBlockHeight
// For confirmation PoC: TriggerHeight
func GetCurrentPocStageHeight(epochState *chainphase.EpochState) int64 {
	if epochState.IsNilOrNotSynced() {
		return 0
	}

	// Confirmation PoC uses event's trigger height
	if epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.CurrentPhase == types.InferencePhase {
		return epochState.ActiveConfirmationPoCEvent.TriggerHeight
	}

	// Regular PoC
	return epochState.LatestEpoch.PocStartBlockHeight
}

// ShouldAcceptStoreCommit returns true if the chain will accept MsgPoCV2StoreCommit
// at the current block height. Mirrors keeper validation.
func ShouldAcceptStoreCommit(epochState *chainphase.EpochState, pocStageStartHeight int64) bool {
	if epochState.IsNilOrNotSynced() {
		return false
	}

	currentHeight := epochState.CurrentBlock.Height

	if epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.CurrentPhase == types.InferencePhase &&
		pocStageStartHeight == epochState.ActiveConfirmationPoCEvent.TriggerHeight {
		event := epochState.ActiveConfirmationPoCEvent
		epochParams := &epochState.LatestEpoch.EpochParams
		return event.IsInBatchSubmissionWindow(currentHeight, epochParams)
	}

	// Regular PoC: check exchange window
	if epochState.CurrentPhase != types.PoCGeneratePhase &&
		epochState.CurrentPhase != types.PoCGenerateWindDownPhase {
		return false
	}

	if !epochState.LatestEpoch.IsStartOfPocStage(pocStageStartHeight) {
		return false
	}

	return epochState.LatestEpoch.IsPoCExchangeWindow(currentHeight)
}

// ShouldHaveDistributedWeights returns true if weights should have been distributed.
// True during Validation phase or WindDown phase.
func ShouldHaveDistributedWeights(epochState *chainphase.EpochState) bool {
	if epochState.IsNilOrNotSynced() {
		return false
	}

	// Regular PoC: Validation or WindDown phases
	if epochState.CurrentPhase == types.PoCValidatePhase ||
		epochState.CurrentPhase == types.PoCValidateWindDownPhase ||
		epochState.CurrentPhase == types.PoCGenerateWindDownPhase {
		return true
	}

	// Confirmation PoC: Validation phase
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		return true
	}

	return false
}
