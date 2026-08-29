package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"devshard/accounting"
	"devshard/user"
)

// The picker burns throttled ghosts in a tight loop, so an ungated probe would flood the host that
// just reported overload.
const (
	throttleProbeMinInterval = 5 * time.Second
	throttleProbeTimeout     = 30 * time.Second
)

// throttleProbeVerdict is what one burn should do about the host it cannot be sent to.
type throttleProbeVerdict int

const (
	throttleProbeSend     throttleProbeVerdict = iota // spend this burn on a probe
	throttleProbeWait                                 // a probe is already asking; its answer decides this burn
	throttleProbeServed                               // the last probe was answered, so the host is not refusing
	throttleProbeUnserved                             // the last probe went unanswered
)

type throttleProbeState struct {
	inFlight   bool
	nextAt     time.Time
	lastServed bool
	waiters    []func(served bool)
}

// throttleProbeGate lets one probe speak for its whole window instead of each burn polling the group.
type throttleProbeGate struct {
	mu     sync.Mutex
	states map[string]*throttleProbeState
}

func (g *throttleProbeGate) decide(participantKey string, now time.Time, onAnswer func(served bool)) throttleProbeVerdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.states == nil {
		g.states = make(map[string]*throttleProbeState)
	}
	state := g.states[participantKey]
	if state == nil {
		state = &throttleProbeState{}
		g.states[participantKey] = state
	}
	if state.inFlight {
		state.waiters = append(state.waiters, onAnswer)
		return throttleProbeWait
	}
	if now.Before(state.nextAt) {
		if state.lastServed {
			return throttleProbeServed
		}
		return throttleProbeUnserved
	}
	state.inFlight = true
	return throttleProbeSend
}

func (g *throttleProbeGate) release(participantKey string, now time.Time, served bool) {
	g.mu.Lock()
	state := g.states[participantKey]
	var waiters []func(served bool)
	if state != nil {
		state.inFlight = false
		state.nextAt = now.Add(throttleProbeMinInterval)
		state.lastServed = served
		waiters, state.waiters = state.waiters, nil
	}
	g.mu.Unlock()
	for _, answer := range waiters {
		answer(served)
	}
}

var throttleProbeEnabled atomic.Bool

func init() { throttleProbeEnabled.Store(true) }

func (e *Redundancy) answerWaitingBurn(prepared *user.PreparedInference, reason string) func(served bool) {
	nonce := prepared.Nonce()
	burnedAt := time.Now()
	hostLabel := e.session.HostLabel(prepared.HostIdx())
	payload := prepared.Payload()
	return func(served bool) {
		ctx, _ := ensureRequestLogContext(context.Background())
		if !served {
			// Each timeout waits out its own deadline and then talks to verifiers, so answering the
			// queue inline would serialize the whole backlog ahead of the escrow's drain barrier.
			e.goTrackedRaceCleanup(func() {
				e.raiseGhostTimeout(ctx, nonce, burnedAt, payload, hostLabel, ghostThrottled)
			})
			return
		}
		// Only a burn booked with a timeout coming has one to retract; with accountability off the
		// nonce already retired at burn time and this would just be rejected.
		if !ghostTimeoutWillBeRaised(ghostThrottled) {
			return
		}
		e.accounting.TimeoutResult(e.devshardID, nonce, timeoutResultKind(user.TimeoutResult{}, nil, false),
			"skipped", string(accounting.TimeoutHostServedProbe),
			string(accounting.TimeoutHostServedProbe), string(accounting.TimeoutHostServedProbe))
		logInferenceStage(ctx, e.devshardID, nonce, "ghost_probe_answered_for", "host", hostLabel, "reason", reason)
	}
}

// sendThrottleProbe asks the host over the channel its users use; one that will not answer earns the
// same timeout the silent burn raised outright.
func (e *Redundancy) sendThrottleProbe(prepared *user.PreparedInference, participantKey, reason string) {
	nonce, hostIdx := prepared.Nonce(), prepared.HostIdx()
	hostLabel := e.session.HostLabel(hostIdx)
	payload := prepared.Payload()
	quarantineMode := e.quarantineModeForParticipant(participantKey)
	sentAt := time.Now()

	// Tagged as ours, so probing cannot move the ratios that rate how a host serves users.
	e.accounting.ProbeSend(e.devshardID, nonce, sentAt, quarantineMode, accounting.DeliveryThrottleProbe)
	if e.metrics != nil {
		e.metrics.RecordGatewaySlotDecision(GatewaySlotDecisionMetric{
			ParticipantKey: participantKey,
			Model:          e.model,
			EscrowID:       e.devshardID,
			Decision:       "ghost_probe_sent",
			Reason:         reason,
			QuarantineMode: quarantineMode,
		})
	}

	e.goTrackedRaceCleanup(func() {
		served := false
		defer func() { e.throttleProbes.release(participantKey, time.Now(), served) }()
		ctx, _ := ensureRequestLogContext(context.Background())
		logInferenceStage(ctx, e.devshardID, nonce, "ghost_probe_sent", "host", hostLabel, "reason", reason)

		sendCtx, cancelSend := context.WithTimeout(ctx, throttleProbeTimeout)
		resp, err := e.session.SendOnly(sendCtx, prepared, nil, nil)
		if err == nil {
			err = e.session.ProcessResponse(hostIdx, resp, nonce)
		}
		cancelSend()
		if err == nil {
			served = true
			e.accounting.ProbeServed(e.devshardID, nonce, accounting.DeliveryThrottleProbe)
			// The quarantine is a guess about a host that has now answered; probation catches it again if
			// the answer was a fluke.
			released := e.participantLimiter != nil && e.participantLimiter.ClearQuarantine(participantKey)
			logInferenceStage(ctx, e.devshardID, nonce, "ghost_probe_served",
				"host", hostLabel, "quarantine_cleared", released)
			return
		}

		logInferenceStage(ctx, e.devshardID, nonce, "ghost_probe_unserved", "host", hostLabel, "error", err)
		e.raiseGhostTimeout(ctx, nonce, sentAt, payload, hostLabel, ghostThrottled)
	})
}
