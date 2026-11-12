package perf

import "time"

// ejectionState is not goroutine-safe; the caller (Tracker) locks around all access.
type ejectionState struct {
	ejectedUntil  time.Time
	ejectionCount int
	wasWithheld   bool
	trigger       ejectionTrigger
}

type ejectionTrigger struct {
	reason              string
	consecutiveFailures int
	failureRate         float64
	failureVolume       float64
}

type ejectionPolicy struct {
	consecutiveFailThreshold int
	failureRateThreshold     float64
	failureRateMinVolume     float64
	base                     time.Duration
	max                      time.Duration
}

func newEjectionPolicy(consecutiveFailThreshold int, failureRateThreshold, failureRateMinVolume float64, base, max time.Duration) ejectionPolicy {
	return ejectionPolicy{
		consecutiveFailThreshold: consecutiveFailThreshold,
		failureRateThreshold:     failureRateThreshold,
		failureRateMinVolume:     failureRateMinVolume,
		base:                     base,
		max:                      max,
	}
}

func (p ejectionPolicy) evaluate(h *hostPerf, state *ejectionState, now time.Time) {
	if state.ejected(now) {
		h.resetOutcomeCounters()
		return
	}
	rate, volume := h.failureRate(now)
	consecutiveTripped := h.consecutiveFail >= p.consecutiveFailThreshold
	rateTripped := volume >= p.failureRateMinVolume && rate >= p.failureRateThreshold
	if !consecutiveTripped && !rateTripped {
		state.decayEjectionCount(p.max, now)
		return
	}
	reason := ejectionReasonFailureRate
	if consecutiveTripped {
		reason = ejectionReasonConsecutiveFailures
	}
	state.ejectionCount++
	state.ejectedUntil = now.Add(min(p.base*time.Duration(state.ejectionCount), p.max))
	state.trigger = ejectionTrigger{reason: reason, consecutiveFailures: h.consecutiveFail, failureRate: rate, failureVolume: volume}
	h.resetOutcomeCounters()
}

func (s *ejectionState) ejected(now time.Time) bool {
	return now.Before(s.ejectedUntil)
}

// decayEjectionCount relaxes one rung per healthy window, advancing the anchor so it can't cascade.
func (s *ejectionState) decayEjectionCount(window time.Duration, now time.Time) {
	for s.ejectionCount > 0 && !s.ejectedUntil.IsZero() && now.Sub(s.ejectedUntil) >= window {
		s.ejectionCount--
		s.ejectedUntil = s.ejectedUntil.Add(window)
	}
}
