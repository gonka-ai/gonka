package poc

import (
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

func ShouldAcceptGeneratedArtifacts(epochState *chainphase.EpochState) bool {
	if epochState.IsNilOrNotSynced() {
		return false
	}
	if epochState.CurrentPhase == types.PoCGeneratePhase {
		return true
	}
	if epochState.CurrentPhase == types.PoCGenerateWindDownPhase {
		return epochState.LatestEpoch.IsPoCExchangeWindow(epochState.CurrentBlock.Height)
	}
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION {
		event := epochState.ActiveConfirmationPoCEvent
		epochParams := &epochState.LatestEpoch.EpochParams
		return event.IsInBatchSubmissionWindow(epochState.CurrentBlock.Height, epochParams)
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

// EarlyShareCaptureTarget computes the stage height and the "first fraction"
// capture target block height for the active PoC or confirmation-PoC generation
// window. ok is false when no generation window is active or inputs are invalid,
// in which case the early-share guard must skip (fail open).
//
//   - Regular PoC: stage = PocStartBlockHeight, target = stage + fraction*duration.
//   - Confirmation PoC: stage = event.TriggerHeight,
//     target = event.GenerationStartHeight + fraction*duration.
func EarlyShareCaptureTarget(epochState *chainphase.EpochState, firstFraction float64) (stageHeight int64, targetHeight int64, ok bool) {
	if epochState.IsNilOrNotSynced() {
		return 0, 0, false
	}
	if firstFraction <= 0 || firstFraction >= 1 {
		return 0, 0, false
	}
	duration := epochState.LatestEpoch.EpochParams.PocStageDuration
	if duration <= 0 {
		return 0, 0, false
	}
	offset := int64(float64(duration) * firstFraction)

	// Confirmation PoC generation window during the inference phase.
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION {
		event := epochState.ActiveConfirmationPoCEvent
		return event.TriggerHeight, event.GenerationStartHeight + offset, true
	}

	// Regular PoC generation (including the wind-down exchange window).
	if epochState.CurrentPhase != types.PoCGeneratePhase &&
		epochState.CurrentPhase != types.PoCGenerateWindDownPhase {
		return 0, 0, false
	}
	stageHeight = epochState.LatestEpoch.PocStartBlockHeight
	return stageHeight, stageHeight + offset, true
}

func ShouldHaveDistributedWeights(epochState *chainphase.EpochState) bool {
	if epochState.IsNilOrNotSynced() {
		return false
	}
	if epochState.CurrentPhase == types.PoCValidatePhase ||
		epochState.CurrentPhase == types.PoCValidateWindDownPhase {
		return true
	}
	if epochState.CurrentPhase == types.PoCGenerateWindDownPhase {
		return epochState.CurrentBlock.Height >= epochState.LatestEpoch.EndOfPoCGeneration()
	}
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		return true
	}
	return false
}
