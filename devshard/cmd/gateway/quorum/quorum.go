// Package quorum is the arithmetic of a timeout vote, shared by the vote queue and the sweep. See README.md.
package quorum

import "devshard/types"

// ExecutorSlot is the slot that runs a nonce in a group of the given size, as the protocol assigns it.
func ExecutorSlot(nonce uint64, groupSize int) int {
	return int(nonce % uint64(groupSize))
}

// Voteable reports whether a timeout vote on the nonce can gather more accept weight than the threshold. The executor's address is left out of the voters whatever slot of it votes, so its own slots never count.
func Voteable(group []types.SlotAssignment, threshold uint32, nonce uint64) bool {
	if len(group) == 0 {
		return true
	}
	executor := group[ExecutorSlot(nonce, len(group))].ValidatorAddress
	var voters uint32
	for _, slot := range group {
		if slot.ValidatorAddress != executor {
			voters++
		}
	}
	return voters > threshold
}
