package broker

import (
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

// challengeOverlay is the broker's view of open challenges.
// poc registers it in init to avoid an import cycle.
type challengeOverlay interface {
	Self() string
	Own(addr string) *types.OpenPoCChallenge
	UnderChallenge() *types.OpenPoCChallenge
}

var overlay challengeOverlay

func SetChallengeOverlay(o challengeOverlay) {
	overlay = o
}

func overlayUnderChallenge() *types.OpenPoCChallenge {
	if overlay == nil {
		return nil
	}
	return overlay.UnderChallenge()
}

func overlayGeneratingChallengeWork(epochState *chainphase.EpochState) *types.OpenPoCChallenge {
	if epochState == nil || epochState.IsNilOrNotSynced() || epochState.IsPoCVoteWindow() {
		return nil
	}
	ch := overlayUnderChallenge()
	if ch == nil || ch.StartHeight() <= 0 {
		return nil
	}
	if ch.Finish <= 0 || epochState.CurrentBlock.Height >= ch.Finish {
		return nil
	}
	return ch
}

func overlayInCommitLead(epochState *chainphase.EpochState) bool {
	ch := overlayGeneratingChallengeWork(epochState)
	if ch == nil || ch.Finish <= 0 {
		return false
	}
	return epochState.CurrentBlock.Height >= ch.Finish-ChallengeCommitLeadBlocksValue()
}

// overlayNothingPreserved is true while this participant is under challenge.
func overlayNothingPreserved() bool {
	return overlayUnderChallenge() != nil
}
