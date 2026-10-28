package limits

import (
	"maps"
	"math/rand"
	"sync"
	"time"
)

type Verdict int

// See README.md, "What blames which window".
const (
	Success Verdict = iota
	Overload
	UpstreamFault
	TransportFault
	ModelOutcome
	MissedReceiptDeadline
	MissedFirstTokenDeadline
	LateSuccess
	EmptyAnswer
	EmptyAnswerLeftOpen
	DecodeStalled
)

// cutoffNarrator is satisfied by *journal.Journal; it is called under the limiter's lock, so it must queue and return. See README.md, "When a host stops taking work".
type cutoffNarrator interface {
	HostCutOff(participant, model, reason string, backoffCount int, cutOffFor time.Duration)
	HostCutOffLifted(participant, model string, backoffCount int)
}

type ParticipantLimiter struct {
	mu              sync.RWMutex
	cfg             ParticipantConfig
	reportedContext map[string]int64
	observedContext map[string]int64
	observedWeights map[string]map[string]float64
	states          map[key]*hostState
	now             func() time.Time
	jitter          func(time.Duration) time.Duration
	lastSweep       time.Time
	narrator        cutoffNarrator
}

func NewParticipantLimiter(cfg ParticipantConfig, now func() time.Time) *ParticipantLimiter {
	return &ParticipantLimiter{
		cfg:    cfg,
		states: make(map[key]*hostState),
		now:    now,
		jitter: defaultJitter,
	}
}

// SetNarrator binds the journal the limiter's cut-off edges are written through; call it before the limiter is shared.
func (l *ParticipantLimiter) SetNarrator(narrator cutoffNarrator) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.narrator = narrator
}

const (
	cutoffBackoffMultiplier = 1.6
	cutoffBackoffPerJitter  = 5
)

// defaultJitter: up to 20% of base (gRPC connection-backoff's JITTER 0.2), so reopened cutoffs don't retry in lockstep.
func defaultJitter(base time.Duration) time.Duration {
	span := int64(base) / cutoffBackoffPerJitter
	if span <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(span))
}

// Reconfigure keeps a window already earned or narrowed, lifts one only past a raised initial size, and never leaves one under the new floor. See capacity.md, "The participant limiter: IOCW".
func (l *ParticipantLimiter) Reconfigure(cfg ParticipantConfig) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg = cfg
	for tracked, state := range l.states {
		previous := state.bounds
		state.bounds = l.windowsForLocked(tracked.participant, tracked.model)
		state.input.raiseTo(liftedFloor(previous.Input, state.bounds.Input))
		state.output.raiseTo(liftedFloor(previous.Output, state.bounds.Output))
	}
}

func liftedFloor(previous, next WindowBounds) int64 {
	if next.Initial > previous.Initial {
		return next.Initial
	}
	return next.Min
}

func (l *ParticipantLimiter) stateLocked(k key) *hostState {
	state, ok := l.states[k]
	if !ok {
		state = l.freshState(k.participant, k.model)
		l.states[k] = state
	}
	return state
}

// ObserveWeights takes the weight the chain gave each host for each model, which is what its windows are
// sized on. See capacity.md, "What a host's weight buys".
func (l *ParticipantLimiter) ObserveWeights(byModel map[string]map[string]float64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.observedWeights = byModel
	for tracked, state := range l.states {
		bounds := l.windowsForLocked(tracked.participant, tracked.model)
		previous := state.bounds
		state.bounds = bounds
		state.input.raiseTo(bounds.Input.Min)
		if bounds.Input.Initial < previous.Input.Initial {
			state.input.lowerTo(bounds.Input.Initial)
		}
		state.output.raiseTo(bounds.Output.Min)
		if bounds.Output.Initial < previous.Output.Initial {
			state.output.lowerTo(bounds.Output.Initial)
		}
	}
}

// ObserveModels takes the context length governance reports for each model.
func (l *ParticipantLimiter) ObserveModels(contextTokens map[string]int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.observedContext == nil {
		l.observedContext = make(map[string]int64, len(contextTokens))
	}
	maps.Copy(l.observedContext, contextTokens)
	l.reportedContext = maps.Clone(contextTokens)
	for tracked, state := range l.states {
		state.bounds = l.windowsForLocked(tracked.participant, tracked.model)
		state.input.raiseTo(state.bounds.Input.Min)
		state.output.raiseTo(state.bounds.Output.Min)
	}
}

func (l *ParticipantLimiter) freshState(participant, model string) *hostState {
	bounds := l.windowsForLocked(participant, model)
	return &hostState{
		bounds: bounds,
		input:  window{tokens: float64(bounds.Input.Initial)},
		output: window{tokens: float64(bounds.Output.Initial)},
	}
}

// smallestRequest is what Admits peeks with: a host that cannot take one token of each has no headroom at all.
var smallestRequest = TokenCost{Input: 1, Output: 1}

func admissionLocked(state *hostState, cost TokenCost, now time.Time) Admission {
	switch cutoffState(state, now) {
	case CutoffOpen:
		return AdmissionCutOff
	case CutoffHalfOpen:
		if state.idle() {
			return AdmissionOpen
		}
		return AdmissionCutOff
	}
	if state.idle() {
		return AdmissionOpen
	}
	if fits(state.input.inflight, cost.Input, state.input.tokens) &&
		fits(state.output.inflight, cost.Output, state.output.tokens) {
		return AdmissionOpen
	}
	return AdmissionWindowFull
}

// Acquire takes the tokens one attempt needs and hands back the release that gives them back exactly once, or the admission that refused them.
func (l *ParticipantLimiter) Acquire(participant, model string, cost TokenCost) (func(), Admission) {
	return l.take(participant, model, cost, false)
}

// Overdraft takes the tokens whether or not they fit, and is refused only by a cut-off. See routing.md, "The forced send".
func (l *ParticipantLimiter) Overdraft(participant, model string, cost TokenCost) (func(), Admission) {
	return l.take(participant, model, cost, true)
}

func (l *ParticipantLimiter) take(participant, model string, cost TokenCost, overFullWindow bool) (func(), Admission) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	tracked := key{participant: participant, model: model}
	state := l.stateLocked(tracked)
	state.lastUsed = now
	l.forgetIdleLocked(now)

	switch refusedBy := admissionLocked(state, cost, now); refusedBy {
	case AdmissionCutOff:
		return nil, refusedBy
	case AdmissionWindowFull:
		if !overFullWindow {
			return nil, refusedBy
		}
	}
	state.probeIfCutOff()
	state.input.take(max(cost.Input, 0))
	state.output.take(max(cost.Output, 0))
	return l.releaseFor(tracked, cost), AdmissionOpen
}

func (l *ParticipantLimiter) releaseFor(tracked key, cost TokenCost) func() {
	var once sync.Once
	return func() {
		once.Do(func() { l.release(tracked, cost) })
	}
}

func (l *ParticipantLimiter) release(tracked key, cost TokenCost) {
	l.mu.Lock()
	defer l.mu.Unlock()

	state, ok := l.states[tracked]
	if !ok {
		return
	}
	state.input.give(max(cost.Input, 0))
	state.output.give(max(cost.Output, 0))
	state.lastUsed = l.now()
}

// Available peeks whether a host would take the smallest request, mutating nothing.
func (l *ParticipantLimiter) Available(participant, model string) bool {
	return l.Admits(participant, model) == AdmissionOpen
}

// Admits peeks why a host would refuse the smallest request, mutating nothing.
func (l *ParticipantLimiter) Admits(participant, model string) Admission {
	l.mu.RLock()
	defer l.mu.RUnlock()

	state, ok := l.states[key{participant: participant, model: model}]
	if !ok {
		return AdmissionOpen
	}
	return admissionLocked(state, smallestRequest, l.now())
}

// See capacity.md, "Nothing here is persisted".
func (l *ParticipantLimiter) forgetIdleLocked(now time.Time) {
	idleFor := l.cfg.IdleEviction
	if idleFor <= 0 || now.Sub(l.lastSweep) < idleFor/10 {
		return
	}
	l.lastSweep = now
	for tracked, state := range l.states {
		if state.idle() && !now.Before(state.openUntil) && now.Sub(state.lastUsed) > idleFor {
			delete(l.states, tracked)
		}
	}
}
