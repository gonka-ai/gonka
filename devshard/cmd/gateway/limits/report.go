package limits

import (
	"cmp"
	"slices"
	"time"

	"devshard/cmd/gateway/internal/safemath"
)

// ModelConcurrency is how many requests at full price the hosts of one model can take, "What a host's weight buys".
func (l *ParticipantLimiter) ModelConcurrency(model string) int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	var requests int64
	for tracked, state := range l.states {
		if tracked.model != model || cutoffState(state, now) == CutoffOpen || state.bounds.Output.Step <= 0 {
			continue
		}
		requests = safemath.AddSaturating(requests, int64(state.output.tokens)/state.bounds.Output.Step)
	}
	return requests
}

// WindowFor answers one pair without walking every tracked one, for a reader asking about a single host.
func (l *ParticipantLimiter) WindowFor(participant, model string) (HostWindow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	state, tracked := l.states[key{participant: participant, model: model}]
	if !tracked {
		return HostWindow{}, false
	}
	return l.windowOf(key{participant: participant, model: model}, state, l.now()), true
}

// HostWindow is one tracked participant/model pair as a reader sees it.
type HostWindow struct {
	Participant          string
	Model                string
	InputWindowTokens    float64
	OutputWindowTokens   float64
	InflightInputTokens  int64
	InflightOutputTokens int64
	Cutoff               CutoffState
	BackoffCount         int
	Available            bool
}

func (l *ParticipantLimiter) windowOf(tracked key, state *hostState, now time.Time) HostWindow {
	return HostWindow{
		Participant:          tracked.participant,
		Model:                tracked.model,
		InputWindowTokens:    state.input.tokens,
		OutputWindowTokens:   state.output.tokens,
		InflightInputTokens:  state.input.inflight,
		InflightOutputTokens: state.output.inflight,
		Cutoff:               cutoffState(state, now),
		BackoffCount:         state.backoffCount,
		Available:            admissionLocked(state, smallestRequest, now) == AdmissionOpen,
	}
}

// Snapshot returns every tracked pair in participant/model order, copied under one lock and sorted after it releases.
func (l *ParticipantLimiter) Snapshot() []HostWindow {
	l.mu.Lock()
	now := l.now()
	windows := make([]HostWindow, 0, len(l.states))
	for tracked, state := range l.states {
		windows = append(windows, l.windowOf(tracked, state, now))
	}
	l.mu.Unlock()

	slices.SortFunc(windows, func(first, second HostWindow) int {
		return cmp.Or(cmp.Compare(first.Participant, second.Participant), cmp.Compare(first.Model, second.Model))
	})
	return windows
}
