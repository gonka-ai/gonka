package limits

import (
	"math"
	"time"
)

func cutoffState(state *hostState, now time.Time) CutoffState {
	switch {
	case now.Before(state.openUntil):
		return CutoffOpen
	case state.halfOpen || !state.openUntil.IsZero():
		return CutoffHalfOpen
	}
	return CutoffClosed
}

// ClearQuarantine reopens one participant's cutoffs. See capacity.md, "The participant limiter: IOCW".
func (l *ParticipantLimiter) ClearQuarantine(participant string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cleared := false
	for tracked, state := range l.states {
		if tracked.participant != participant {
			continue
		}
		state.input.reopen(state.bounds.Input.Initial)
		state.output.reopen(state.bounds.Output.Initial)
		state.clearBreaker()
		cleared = true
	}
	return cleared
}

// ResetAll reopens every pair's windows at their initial size and closes every cut-off, keeping the tokens still in flight, and reports the pairs reset and how many were cut off; a result of an attempt launched before it is dropped by OnResult. See README.md, "The cut-off, the peek and the sweep".
func (l *ParticipantLimiter) ResetAll() (pairs, cutOff int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetAt = l.now()
	for tracked, state := range l.states {
		state.input.reopen(state.bounds.Input.Initial)
		state.output.reopen(state.bounds.Output.Initial)
		if !state.openUntil.IsZero() {
			cutOff++
			if l.narrator != nil {
				l.narrator.HostCutOffLifted(tracked.participant, tracked.model, state.backoffCount)
			}
		}
		state.clearBreaker()
	}
	return len(l.states), cutOff
}

func cutoffReason(halfOpen bool) string {
	if halfOpen {
		return cutoffReasonProbeFailed
	}
	return cutoffReasonConsecutiveFaults
}

// OnResult applies one finished attempt to the host's windows and its cutoff.
func (l *ParticipantLimiter) OnResult(result Result) {
	answer := responseFor(result.Verdict)
	if answer.inert() {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if launchedBefore(result.LaunchedAt, l.resetAt) {
		return
	}
	state := l.stateLocked(key{participant: result.Participant, model: result.Model})
	now := l.now()
	state.lastUsed = now
	settings := l.cfg.forModel(result.Model)

	narrowingTier, blamed := answer.tier, answer.dimension
	if narrowingTier == tierNone && result.Verdict == Success {
		if congested := result.Pressure.congestedDimension(settings.Slack); congested != dimensionNone {
			narrowingTier, blamed = tierSoft, congested
		} else {
			l.growLocked(state, result.Carried)
		}
	}
	if by := settings.Factors.narrowingFor(narrowingTier, blamed); by.moves() {
		l.narrowLocked(state, by)
	}
	if !launchedBefore(result.LaunchedAt, state.openedAt) {
		l.applyBreakerLocked(state, answer.breaker, result.Participant, result.Model, now)
	}
}

// CountRefusal counts a refusal towards the cut-off when the host carries nothing else of ours for this model, or when the attempt is the half-open probe; one launched before the reset or before the cut-off opened counts for nothing. See README.md, "What blames which window".
func (l *ParticipantLimiter) CountRefusal(participant, model string, launchedAt time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if launchedBefore(launchedAt, l.resetAt) {
		return
	}

	state := l.stateLocked(key{participant: participant, model: model})
	now := l.now()
	state.lastUsed = now
	if launchedBefore(launchedAt, state.openedAt) || (!state.idle() && !state.halfOpen) {
		return
	}
	l.applyBreakerLocked(state, breakerCounts, participant, model, now)
}

// growLocked widens each window whose peak earned it. See README.md, "Additive increase".
func (l *ParticipantLimiter) growLocked(state *hostState, carried TokenCost) {
	state.input.growIfEarned(carried.Input, state.bounds.Input.Step)
	state.output.growIfEarned(carried.Output, state.bounds.Output.Step)
}

// narrowLocked applies one signal's factors to the two windows.
func (l *ParticipantLimiter) narrowLocked(state *hostState, by narrowing) {
	state.input.narrow(by.input, state.bounds.Input.Min)
	state.output.narrow(by.output, state.bounds.Output.Min)
}

func (l *ParticipantLimiter) applyBreakerLocked(state *hostState, effect breakerEffect, participant, model string, now time.Time) {
	switch effect {
	case breakerRecovers:
		if !state.recoverBreaker() {
			return
		}
		if l.narrator != nil {
			l.narrator.HostCutOffLifted(participant, model, state.backoffCount)
		}
	case breakerCounts:
		settings := l.cfg.forModel(model)
		if !state.recordCutoffFault(now, int(settings.AfterFailures)) {
			return
		}
		capped := min(time.Duration(float64(settings.BaseOpen)*math.Pow(cutoffBackoffMultiplier, float64(state.backoffCount))), settings.MaxOpen)
		reason := state.openBreaker(now, now.Add(capped+l.jitter(capped)), capped < settings.MaxOpen)
		if l.narrator != nil {
			l.narrator.HostCutOff(participant, model, reason, state.backoffCount, state.openUntil.Sub(now))
		}
	}
}
