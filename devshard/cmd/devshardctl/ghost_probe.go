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

type throttleProbeState struct {
	inFlight bool
	nextAt   time.Time
}

// throttleProbeGate bounds ghost probes to one in flight per participant. The zero value is usable.
type throttleProbeGate struct {
	mu     sync.Mutex
	states map[string]throttleProbeState
}

func (g *throttleProbeGate) admit(participantKey string, now time.Time) bool {
	if participantKey == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.states == nil {
		g.states = make(map[string]throttleProbeState)
	}
	state := g.states[participantKey]
	if state.inFlight || now.Before(state.nextAt) {
		return false
	}
	g.states[participantKey] = throttleProbeState{inFlight: true, nextAt: now.Add(throttleProbeMinInterval)}
	return true
}

func (g *throttleProbeGate) release(participantKey string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.states == nil {
		return
	}
	g.states[participantKey] = throttleProbeState{nextAt: now.Add(throttleProbeMinInterval)}
}

// throttleProbeEnabled is off unless something turns it on: probing puts traffic on an overloaded host.
var throttleProbeEnabled atomic.Bool

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
		defer e.throttleProbes.release(participantKey, time.Now())
		ctx, _ := ensureRequestLogContext(context.Background())
		logInferenceStage(ctx, e.devshardID, nonce, "ghost_probe_sent", "host", hostLabel, "reason", reason)

		sendCtx, cancelSend := context.WithTimeout(ctx, throttleProbeTimeout)
		resp, err := e.session.SendOnly(sendCtx, prepared, nil, nil)
		if err == nil {
			err = e.session.ProcessResponse(hostIdx, resp, nonce)
		}
		cancelSend()
		if err == nil {
			e.accounting.ProbeServed(e.devshardID, nonce, accounting.DeliveryThrottleProbe)
			logInferenceStage(ctx, e.devshardID, nonce, "ghost_probe_served", "host", hostLabel)
			return
		}

		logInferenceStage(ctx, e.devshardID, nonce, "ghost_probe_unserved", "host", hostLabel, "error", err)
		e.raiseGhostTimeout(ctx, nonce, sentAt, payload, hostLabel, ghostThrottled)
	})
}
