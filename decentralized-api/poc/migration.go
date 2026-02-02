package poc

import (
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

func IsMigrationMode(pocV2Enabled, confirmationPocV2Enabled bool) bool {
	return !pocV2Enabled && confirmationPocV2Enabled
}

func ShouldUseV2(pocV2Enabled, confirmationPocV2Enabled bool, confirmationEvent *types.ConfirmationPoCEvent) bool {
	if pocV2Enabled {
		return true
	}
	if IsMigrationMode(pocV2Enabled, confirmationPocV2Enabled) &&
		confirmationEvent != nil && confirmationEvent.EventSequence == 0 {
		return true
	}
	return false
}

func ShouldUseV2FromEpochState(epochState *chainphase.EpochState) bool {
	if epochState == nil {
		return true
	}
	return ShouldUseV2(
		epochState.PocV2Enabled,
		epochState.ConfirmationPocV2Enabled,
		epochState.ActiveConfirmationPoCEvent,
	)
}
