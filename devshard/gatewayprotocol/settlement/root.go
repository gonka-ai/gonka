package settlement

import (
	"devshard/state"
	"devshard/types"
)

// Root hashes payload.HostStats exactly as given into the settlement state root; it never filters.
func Root(payload state.SettlementPayload) ([]byte, error) {
	hostStatsHash, err := state.ComputeHostStatsHash(payload.HostStats)
	if err != nil {
		return nil, err
	}
	return state.ComputeStateRootFromRestHash(hostStatsHash, payload.RestHash, payload.Fees, types.PhaseSettlement, payload.StateRootAndProtocolVersion), nil
}

// PresentStats drops nil entries from perSlot, returning a new map.
func PresentStats(perSlot map[uint32]*types.HostStats) map[uint32]*types.HostStats {
	present := make(map[uint32]*types.HostStats, len(perSlot))
	for slotID, hostStats := range perSlot {
		if hostStats == nil {
			continue
		}
		present[slotID] = hostStats
	}
	return present
}
