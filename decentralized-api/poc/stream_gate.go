package poc

import "decentralized-api/chainphase"

type StreamPhaseGate struct {
	tracker *chainphase.ChainPhaseTracker
}

func NewStreamPhaseGate(tracker *chainphase.ChainPhaseTracker) *StreamPhaseGate {
	return &StreamPhaseGate{tracker: tracker}
}

func (g *StreamPhaseGate) StreamActive() bool {
	epochState := g.tracker.GetCurrentEpochState()
	return ShouldAcceptGeneratedArtifacts(epochState) || ShouldAcceptValidatedArtifacts(epochState)
}
