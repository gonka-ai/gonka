package settlement

import (
	"maps"
	"slices"

	"devshard/types"
)

// HostStatsBySlot lists the host stats in ascending slot order; every entry must be non-nil.
func HostStatsBySlot(perSlot map[uint32]*types.HostStats) []HostStat {
	stats := make([]HostStat, 0, len(perSlot))
	for _, slotID := range slices.Sorted(maps.Keys(perSlot)) {
		hostStats := perSlot[slotID]
		stats = append(stats, HostStat{
			SlotID:               slotID,
			Missed:               hostStats.Missed,
			Invalid:              hostStats.Invalid,
			Cost:                 hostStats.Cost,
			RequiredValidations:  hostStats.RequiredValidations,
			CompletedValidations: hostStats.CompletedValidations,
			Validated:            hostStats.Validated,
			Finished:             hostStats.Finished,
		})
	}
	return stats
}

// SignaturesBySlot lists the slot signatures in ascending slot order.
func SignaturesBySlot(perSlot map[uint32][]byte) []SlotSignature {
	signatures := make([]SlotSignature, 0, len(perSlot))
	for _, slotID := range slices.Sorted(maps.Keys(perSlot)) {
		signatures = append(signatures, SlotSignature{SlotID: slotID, Signature: perSlot[slotID]})
	}
	return signatures
}
