package subnet

import "decentralized-api/chainphase"

func currentEpochID(tracker *chainphase.ChainPhaseTracker) uint64 {
	state := tracker.GetCurrentEpochState()
	if state != nil {
		return state.LatestEpoch.EpochIndex
	}
	return 0
}
