package scheduler

import (
	"time"
)

// availability is the host predicates for one drain. Its frozen map memoises the whole ladder per participant, and a nil one leaves every read live. See routing.md, "The drain".
type availability struct {
	pocRequired  func(participant string) bool
	congested    func(participant string) blockReason
	ejected      func(participant string) bool
	notAllowed   func(participant string) bool
	stateBlocked func(participant string) bool
	absent       func(participant string) bool
	budgetSpent  func(participant string) bool
	unthrottled  func(participant string) bool

	frozen          map[string]blockReason
	overFullWindows bool
}

type blockReason int

const (
	blockNone blockReason = iota
	blockPoCRequired
	blockWindowFull
	blockCutOff
	blockEjected
	blockNotAllowed
	blockExcluded
	blockStateDiverged
	blockAbsentFromChain
)

// ghostFor names the burn each participant-only block earns, as one mapping rather than two switches.
var ghostFor = map[blockReason]GhostKind{
	blockPoCRequired:     ghostPoC,
	blockWindowFull:      ghostWindowFull,
	blockCutOff:          ghostCutOff,
	blockEjected:         ghostEjected,
	blockNotAllowed:      ghostNotAllowed,
	blockStateDiverged:   ghostStateDiverged,
	blockAbsentFromChain: ghostAbsentFromChain,
}

// A missing predicate reads as no block, so an availability built without one does not narrow dispatch.
func (a availability) divergedFromEscrowState(participant string) bool {
	return a.stateBlocked != nil && a.stateBlocked(participant)
}

func (a availability) absentFromSession(participant string) bool {
	return a.absent != nil && a.absent(participant)
}

func (a availability) outsideAllowlist(participant string) bool {
	return a.notAllowed != nil && a.notAllowed(participant)
}

// throttlingWaived waives the rungs protecting a host from this gateway, never those protecting the nonce. See routing.md, "Unthrottled participants".
func (a availability) throttlingWaived(participant string) bool {
	return a.unthrottled != nil && a.unthrottled(participant)
}

// participantBlocked is the half of blocks that needs no waiter, so match and blocks share one ladder.
func (a availability) participantBlocked(participant string) blockReason {
	reason := a.memoisedBlock(participant)
	if reason == blockWindowFull && a.overFullWindows {
		return blockNone
	}
	return reason
}

func (a availability) memoisedBlock(participant string) blockReason {
	if reason, memoised := a.frozen[participant]; memoised {
		return reason
	}
	reason := a.firstBlock(participant)
	if a.frozen != nil {
		a.frozen[participant] = reason
	}
	return reason
}

// firstBlock stops at the first rung that holds, so no rung below it is asked.
func (a availability) firstBlock(participant string) blockReason {
	if a.outsideAllowlist(participant) {
		return blockNotAllowed
	}
	if a.absentFromSession(participant) {
		return blockAbsentFromChain
	}
	waived := a.throttlingWaived(participant)
	if a.pocRequired(participant) {
		return blockPoCRequired
	}
	if limited := a.congestionBlock(participant); limited == blockCutOff || (limited != blockNone && !waived) {
		return limited
	}
	if !waived && a.ejected(participant) {
		return blockEjected
	}
	if a.divergedFromEscrowState(participant) {
		return blockStateDiverged
	}
	return blockNone
}

func (a availability) congestionBlock(participant string) blockReason {
	if a.congested != nil {
		if reason := a.congested(participant); reason != blockNone {
			return reason
		}
	}
	if a.budgetSpent != nil && a.budgetSpent(participant) {
		return blockWindowFull
	}
	return blockNone
}

// forEscrow adds the rungs that belong to a single escrow rather than to the fleet, its state block and its hosts' chat budgets, and must be applied before the ladder is frozen. See routing.md, "Pricing an escrow by the burns it will cost".
func (a availability) forEscrow(stateBlocked, absent, budgetSpent func(participant string) bool) availability {
	a.stateBlocked = stateBlocked
	a.absent = absent
	a.budgetSpent = budgetSpent
	return a
}

// servingOverFullWindows shares this drain's memo and stops reading a full window as a block. See routing.md, "The forced send".
func (a availability) servingOverFullWindows() availability {
	a.overFullWindows = true
	return a
}

// refuseSlot folds a refused admission into the frozen ladder, under the reason that refused it.
func (a availability) refuseSlot(participant string, reason blockReason) {
	if a.frozen != nil {
		a.frozen[participant] = reason
	}
}

// blocks is the one definition of "this participant cannot serve this waiter", read by match and servable alike. See README, "The drain".
func (a availability) blocks(participant string, queued *waiter) blockReason {
	if reason := a.participantBlocked(participant); reason != blockNone {
		return reason
	}
	if queued.exclude[participant] {
		return blockExcluded
	}
	return blockNone
}

// onlyThisHostIsLeft asks about the fleet as it stands, so a binding allowed to cross a full window does not read every other full host as usable. See routing.md, "Serving a host the request excluded".
func (a availability) onlyThisHostIsLeft(participant string, participants []string, queued *waiter) bool {
	a.overFullWindows = false
	for _, other := range participants {
		if other != participant && a.blocks(other, queued) == blockNone {
			return false
		}
	}
	return true
}

// match is pure and total, and filters by participant rather than slot. See README, "The match decision".
func match(binding HostBinding, waiting []*waiter, participants []string, avail availability, now time.Time, matchWait time.Duration) Decision {
	participant := binding.Participant
	if reason := avail.participantBlocked(participant); reason != blockNone {
		return burn{kind: ghostFor[reason]}
	}

	var oldestLive, excludedOnly *waiter
	for _, queued := range waiting {
		if queued.abandoned.Load() {
			continue
		}
		if oldestLive == nil {
			oldestLive = queued
		}
		switch avail.blocks(participant, queued) {
		case blockNone:
			return serve{waiter: queued}
		case blockExcluded:
			if excludedOnly == nil && avail.onlyThisHostIsLeft(participant, participants, queued) {
				excludedOnly = queued
			}
		}
	}

	if oldestLive == nil {
		return decline{}
	}
	if until := oldestLive.enqueued.Add(matchWait); now.Before(until) {
		return hold{until: until}
	}
	// See routing.md, "Serving a host the request excluded".
	if excludedOnly != nil {
		return serve{waiter: excludedOnly, despiteExclusion: true}
	}
	return burn{kind: ghostExclude}
}
