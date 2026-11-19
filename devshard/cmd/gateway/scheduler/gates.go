package scheduler

import (
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/limits"
)

// predicates rebuilds the host filters on every drain; the dispatcher freezes the result for that drain.
func (s *Scheduler) predicates(escrow Escrow) func(chain.PhaseSnapshot) availability {
	model, escrowID := escrow.Model, escrow.ID
	return func(snapshot chain.PhaseSnapshot) availability {
		return s.fleetGates(model, snapshot).forEscrow(s.stateBlocked(escrowID), s.absentFromChain(escrowID), chatBudgetSpent(escrow))
	}
}

// fleetGates is the part of the ladder that depends on the model alone. See routing.md, "Pricing an escrow by the burns it will cost".
func (s *Scheduler) fleetGates(model string, snapshot chain.PhaseSnapshot) availability {
	preserved := pocPreserved(snapshot, model)
	allowlist, unthrottled := s.participantAllowlist(), s.unthrottledParticipants()
	return availability{
		notAllowed:  refusedByAllowlist(allowlist, unthrottled),
		unthrottled: waivesThrottling(unthrottled),
		pocRequired: func(participant string) bool { return preserved != nil && !preserved[participant] },
		congested:   func(participant string) blockReason { return blockForAdmission(s.limiter.Admits(participant, model)) },
		ejected:     func(participant string) bool { return s.perf.Ejected(participant, model) },
	}
}

func chatBudgetSpent(escrow Escrow) func(participant string) bool {
	if escrow.Session == nil {
		return nil
	}
	return escrow.Session.ChatBudgetSpent
}

// blockForAdmission maps the limiter's verdict onto the drain's ladder, so only a full window is ever forced through. See routing.md, "The forced send".
func blockForAdmission(admission limits.Admission) blockReason {
	switch admission {
	case limits.AdmissionWindowFull:
		return blockWindowFull
	case limits.AdmissionCutOff:
		return blockCutOff
	}
	return blockNone
}

func (s *Scheduler) acquireSlot(escrow Escrow) func(participant string, cost limits.TokenCost, overFullWindow bool) (func(), limits.Admission) {
	model := escrow.Model
	return func(participant string, cost limits.TokenCost, overFullWindow bool) (func(), limits.Admission) {
		if overFullWindow {
			return s.limiter.Overdraft(participant, model, cost)
		}
		return s.limiter.Acquire(participant, model, cost)
	}
}

// slotCost prices one request against a host's congestion windows: the input it must prefill, the output it may produce.
func slotCost(profile RequestProfile) limits.TokenCost {
	return limits.TokenCost{
		Input:  int64(max(profile.InputTokens, 0)),
		Output: int64(max(profile.OutputTokens, 0)),
	}
}

// stateBlocked reads the blocks live: the drain asks once per participant, and a block that lands while it runs must reach the hosts it has not offered yet.
func (s *Scheduler) stateBlocked(escrowID string) func(string) bool {
	return func(participant string) bool {
		s.blocksMu.RLock()
		defer s.blocksMu.RUnlock()
		return s.blockedHosts[escrowID][participant]
	}
}

func (s *Scheduler) absentFromChain(escrowID string) func(string) bool {
	return func(participant string) bool {
		s.blocksMu.RLock()
		defer s.blocksMu.RUnlock()
		return s.absentHosts[escrowID][participant]
	}
}

func (s *Scheduler) matchWait() time.Duration {
	return time.Duration(s.settings.Load().Scheduler.MatchWaitMS) * time.Millisecond
}

func (s *Scheduler) dispatchQueueDepth() int {
	return int(s.settings.Load().Scheduler.DispatchQueueDepth)
}

// maxConsecutiveBurns is read per drain rather than per dispatcher, so an admin change reaches an escrow already running. See routing.md, "The forced send".
func (s *Scheduler) maxConsecutiveBurns() int64 {
	return s.settings.Load().Scheduler.MaxConsecutiveBurns
}

// requestReserve prices this one request the way the chain will charge it. See capacity.md, "The balance floor".
func requestReserve(profile RequestProfile) uint64 {
	return uint64(max(profile.InputBytes, 0)) + uint64(max(profile.OutputTokens, 0))
}

// retirementReserve prices the largest request the model's hosts accept, and never less than one capped answer. See capacity.md, "The balance floor".
func (s *Scheduler) retirementReserve(model string, snapshot chain.PhaseSnapshot) uint64 {
	if s.settings == nil {
		return 0
	}
	return s.settings.Load().Limits.RetirementReserve(model, snapshot.Models[model].MaxModelLen)
}

// retirementReserveFor reads the model's floor per call, so a dispatcher follows a pin or a chain length that moves.
func (s *Scheduler) retirementReserveFor(escrow Escrow) func() uint64 {
	return func() uint64 { return s.retirementReserve(escrow.Model, s.snapshots.Snapshot()) }
}

// pocPreserved prefers the model's own set; a nil set means not loaded yet, so everybody counts as preserved. See rules.md, "8. Fail-closed and fail-open are chosen per signal".
func pocPreserved(snapshot chain.PhaseSnapshot, model string) map[string]bool {
	preserved := snapshot.PreservedByModel[model]
	if preserved == nil {
		preserved = snapshot.Preserved
	}
	if preserved == nil {
		return nil
	}
	loaded := make(map[string]bool, len(preserved))
	for _, participant := range preserved {
		loaded[participant] = true
	}
	return loaded
}
