package types

import (
	"cmp"
	"slices"
)

// VotingPowerSliceToMap converts a slice of VotingPowerEntry to a map keyed by address.
func VotingPowerSliceToMap(entries []*VotingPowerEntry) map[string]int64 {
	result := make(map[string]int64, len(entries))
	for _, e := range entries {
		result[e.Address] = e.VotingPower
	}
	return result
}

// VotingPowerMapToSlice converts a map of address->votingPower to a sorted slice
// of VotingPowerEntry, ordered by address for deterministic output.
func VotingPowerMapToSlice(weights map[string]int64) []*VotingPowerEntry {
	result := make([]*VotingPowerEntry, 0, len(weights))
	for addr, w := range weights {
		result = append(result, &VotingPowerEntry{Address: addr, VotingPower: w})
	}
	slices.SortFunc(result, func(a, b *VotingPowerEntry) int {
		return cmp.Compare(a.Address, b.Address)
	})
	return result
}
