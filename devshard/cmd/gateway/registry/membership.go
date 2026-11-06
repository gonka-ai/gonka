package registry

import "devshard/cmd/gateway/scheduler"

// membership is satisfied by *limits.Capacity.
type membership interface {
	SetEscrowMembership(escrowID string, hostShares map[string]float64)
	RemoveEscrow(escrowID string)
}

// exhaustion carries routing's two rotation notices, a spent escrow and a taken reserve; satisfied by *escrow.Manager. Called from the request path, so it must mark and return, not do I/O.
type exhaustion interface {
	OnBalanceExhausted(escrowID string, reason scheduler.ExhaustionReason)
	OnReserveTaken(escrowID string)
}

// publications is called while the registry holds its lock, so an implementation must return without doing work.
type publications interface {
	EscrowPublished(escrowID, model string)
}

// escrowNarrator is satisfied by *journal.Journal; Add and unpublish call it under the registry lock, so it must queue and return. See README.md, "Publishing, retiring and draining".
type escrowNarrator interface {
	EscrowServing(escrowID, model string)
	EscrowRetired(escrowID string)
	EscrowRetiredDraining(escrowID string, inFlight int64)
	DrainingEscrowClosed(escrowID string, closeErr error)
	RetirementPendingFlushed(escrowID string, pending int, err error)
	SettlementUnverifiable(escrowID string, nonce uint64, unverifiable error)
}

// slotCounts counts each participant's slots: one holding several repeats in the per-slot key list.
func slotCounts(perSlotKeys []string) map[string]int {
	counts := make(map[string]int, len(perSlotKeys))
	for _, participant := range perSlotKeys {
		if participant == "" {
			continue
		}
		counts[participant]++
	}
	return counts
}

// hostShares is slots(participant, escrow)/totalSlots(participant). See routing.md, "Membership: what the capacity model is told".
func hostShares(escrowSlots, totalSlots map[string]int) map[string]float64 {
	shares := make(map[string]float64, len(escrowSlots))
	for participant, count := range escrowSlots {
		total := totalSlots[participant]
		if count <= 0 || total <= 0 {
			continue
		}
		shares[participant] = float64(count) / float64(total)
	}
	return shares
}
