package broker

import (
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

// challengeOverlay is the process-wide OpenPoCChallenges view. poc registers
// it in init so broker can overlay StartPoc / InitValidate / prefetch without
// importing the poc package (poc already imports broker).
type challengeOverlay interface {
	Self() string
	Own(addr string) *types.OpenPoCChallenge
	SelfGenerating() *types.OpenPoCChallenge
}

var overlay challengeOverlay

func SetChallengeOverlay(o challengeOverlay) {
	overlay = o
}

func overlayGenerating() *types.OpenPoCChallenge {
	if overlay == nil {
		return nil
	}
	return overlay.SelfGenerating()
}

func overlayOwnChallengeGenerate(epochState *chainphase.EpochState) *types.OpenPoCChallenge {
	if epochState == nil || epochState.IsNilOrNotSynced() || epochState.IsPoCVoteWindow() {
		return nil
	}
	ch := overlayGenerating()
	if ch == nil || ch.StartHeight <= 0 {
		return nil
	}
	if ch.Finish <= 0 || epochState.CurrentBlock.Height >= ch.Finish {
		return nil
	}
	return ch
}

func overlayInCommitLead(epochState *chainphase.EpochState) bool {
	ch := overlayOwnChallengeGenerate(epochState)
	if ch == nil || ch.Finish <= 0 {
		return false
	}
	return epochState.CurrentBlock.Height >= ch.Finish-ChallengeCommitLeadBlocksValue()
}

func overlayIgnorePocSlotOnStart(epochState *chainphase.EpochState) bool {
	return overlayOwnChallengeGenerate(epochState) != nil
}

func overlayIgnorePocSlotOnValidate(epochState *chainphase.EpochState) bool {
	return overlayOwnChallengeGenerate(epochState) != nil
}
