package gatewayproto

import (
	"devshard/state"
	"devshard/types"
	"slices"
)

func HostStatForSlot(slot uint32, hs *types.HostStats) SettlementHostStat {
	return SettlementHostStat{SlotID: slot, Missed: hs.Missed, Invalid: hs.Invalid, Cost: hs.Cost,
		RequiredValidations: hs.RequiredValidations, CompletedValidations: hs.CompletedValidations}
}

func StatsPerPresentSlot(perSlot map[uint32]*types.HostStats) map[uint32]*types.HostStats {
	present := make(map[uint32]*types.HostStats, len(perSlot))
	for slot, stats := range perSlot {
		if stats != nil {
			present[slot] = stats
		}
	}
	return present
}

// SortedHostStats is used only by adapters whose existing output sorts slots.
func SortedHostStats(perSlot map[uint32]*types.HostStats) []SettlementHostStat {
	stats := make([]SettlementHostStat, 0, len(perSlot))
	for _, slot := range settlementSlots(perSlot) {
		stats = append(stats, HostStatForSlot(slot, perSlot[slot]))
	}
	return stats
}

func SortedSlotSignatures(perSlot map[uint32][]byte) []SettlementSlotSig {
	sigs := make([]SettlementSlotSig, 0, len(perSlot))
	for _, slot := range settlementSlots(perSlot) {
		sigs = append(sigs, SettlementSlotSig{SlotID: uint64(slot), Signature: perSlot[slot]})
	}
	return sigs
}

func settlementSlots[V any](entries map[uint32]V) []uint32 {
	keys := make([]uint32, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func SettlementRoot(payload state.SettlementPayload, hostStats map[uint32]*types.HostStats) ([]byte, error) {
	hash, err := state.ComputeHostStatsHash(hostStats)
	if err != nil {
		return nil, err
	}
	return state.ComputeStateRootFromRestHash(hash, payload.RestHash, payload.Fees, types.PhaseSettlement, payload.StateRootAndProtocolVersion), nil
}
