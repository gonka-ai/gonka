package main

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"

	"devshard/observability"
)

type lifecyclePhase string

const (
	lifecyclePhaseStarting     lifecyclePhase = "starting"
	lifecyclePhaseServing      lifecyclePhase = "serving"
	lifecyclePhaseDisconnected lifecyclePhase = "disconnected"
	lifecyclePhaseDraining     lifecyclePhase = "draining"
)

type lifecycleEvent string

const (
	lifecycleEventChainReady        lifecycleEvent = "chain_ready"
	lifecycleEventChainDisconnected lifecycleEvent = "chain_disconnected"
	lifecycleEventDrainRequested    lifecycleEvent = "drain_requested"
)

type lifecyclePhaseSpec struct {
	ready       bool
	draining    bool
	accepting   bool
	transitions map[lifecycleEvent]lifecyclePhase
}

var lifecyclePhaseTable = map[lifecyclePhase]lifecyclePhaseSpec{
	lifecyclePhaseStarting: {
		accepting: true,
		transitions: map[lifecycleEvent]lifecyclePhase{
			lifecycleEventChainReady:        lifecyclePhaseServing,
			lifecycleEventChainDisconnected: lifecyclePhaseStarting,
			lifecycleEventDrainRequested:    lifecyclePhaseDraining,
		},
	},
	lifecyclePhaseServing: {
		ready:     true,
		accepting: true,
		transitions: map[lifecycleEvent]lifecyclePhase{
			lifecycleEventChainReady:        lifecyclePhaseServing,
			lifecycleEventChainDisconnected: lifecyclePhaseDisconnected,
			lifecycleEventDrainRequested:    lifecyclePhaseDraining,
		},
	},
	lifecyclePhaseDisconnected: {
		// A chain reconnect is a correlated dependency event: every replica sees
		// it at once. The child has completed initial startup and can still serve
		// its existing shard state, so keep it in rotation while the listener
		// reconnects. Storage readiness remains an independent /ready gate.
		ready:     true,
		accepting: true,
		transitions: map[lifecycleEvent]lifecyclePhase{
			lifecycleEventChainReady:        lifecyclePhaseServing,
			lifecycleEventChainDisconnected: lifecyclePhaseDisconnected,
			lifecycleEventDrainRequested:    lifecyclePhaseDraining,
		},
	},
	lifecyclePhaseDraining: {
		draining: true,
		transitions: map[lifecycleEvent]lifecyclePhase{
			lifecycleEventChainReady:        lifecyclePhaseDraining,
			lifecycleEventChainDisconnected: lifecyclePhaseDraining,
			lifecycleEventDrainRequested:    lifecyclePhaseDraining,
		},
	},
}

type lifecycleState struct {
	mu       sync.Mutex
	phase    lifecyclePhase
	inflight int64
}

type drainStatus struct {
	Ready    bool  `json:"ready"`
	Draining bool  `json:"draining"`
	Inflight int64 `json:"inflight"`
}

func newLifecycleState() *lifecycleState {
	observability.SetLifecycleInflight(0)
	return &lifecycleState{phase: lifecyclePhaseStarting}
}

func (s *lifecycleState) SetReady(ready bool) {
	event := lifecycleEventChainDisconnected
	if ready {
		event = lifecycleEventChainReady
	}
	s.transition(event)
}

func (s *lifecycleState) StartDrain() {
	s.transition(lifecycleEventDrainRequested)
}

func nextLifecyclePhase(
	from lifecyclePhase,
	event lifecycleEvent,
) (lifecyclePhase, bool) {
	spec, ok := lifecyclePhaseTable[from]
	if !ok {
		return "", false
	}
	next, ok := spec.transitions[event]
	return next, ok
}

func (s *lifecycleState) transition(event lifecycleEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, ok := nextLifecyclePhase(s.phase, event)
	if !ok {
		slog.Error(
			"invalid devshard lifecycle transition",
			"from", s.phase,
			"event", event,
		)
		return false
	}
	s.phase = next
	return true
}

func (s *lifecycleState) Status() drainStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, ok := lifecyclePhaseTable[s.phase]
	if !ok {
		return drainStatus{
			Draining: true,
			Inflight: s.inflight,
		}
	}
	return drainStatus{
		Ready:    spec.ready,
		Draining: spec.draining,
		Inflight: s.inflight,
	}
}

func (s *lifecycleState) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, ok := lifecyclePhaseTable[s.phase]
	if !ok || !spec.accepting {
		return false
	}
	s.inflight++
	observability.SetLifecycleInflight(s.inflight)
	return true
}

func (s *lifecycleState) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight == 0 {
		slog.Error("devshard lifecycle request released without admission")
		return
	}
	s.inflight--
	observability.SetLifecycleInflight(s.inflight)
}

func (s *lifecycleState) middleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if isLifecycleBypassPath(c.Request().URL.Path) {
			return next(c)
		}
		if !s.acquire() {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "devshardd is draining")
		}
		defer s.release()
		return next(c)
	}
}

func isLifecycleBypassPath(path string) bool {
	clean := "/" + strings.Trim(strings.TrimSpace(path), "/")
	switch clean {
	case "/healthz", "/metrics":
		return true
	default:
		return false
	}
}
