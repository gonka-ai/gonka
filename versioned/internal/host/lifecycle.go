package host

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

type State string

const (
	StateStarting   State = "starting"
	StateServing    State = "serving"
	StateAnnouncing State = "announcing"
	StateDraining   State = "draining"
	StateStopping   State = "stopping"
	StateForcing    State = "forcing"
	StateStopped    State = "stopped"
)

var ErrInvalidTransition = errors.New("invalid versiond host state transition")

// stateSpec.accepting decides whether new proxy work may acquire a lease.
// stateSpec.ready decides what GET /readyz reports to the load balancer.
//
// announcing accepts work but is already unready: the balancer needs a moment
// to observe the failing health check and stop routing here. Serving through
// that window is what makes it safe to signal the process before the balancer
// has reacted; refusing immediately would surface as client errors instead.
type stateSpec struct {
	accepting bool
	ready     bool
	targets   []State
}

var stateTable = map[State]stateSpec{
	StateStarting: {
		// No path to announcing: announcing keeps accepting while balancers
		// notice, and a starting host has never accepted. Reaching announcing
		// from here would open admission for the first time on the way down,
		// taking work no balancer will see drained. Shutdown from starting goes
		// straight to draining.
		targets: []State{
			StateServing,
			StateDraining,
			StateForcing,
		},
	},
	StateServing: {
		accepting: true,
		ready:     true,
		targets: []State{
			StateAnnouncing,
			StateDraining,
			StateForcing,
		},
	},
	StateAnnouncing: {
		accepting: true,
		targets: []State{
			StateDraining,
			StateForcing,
		},
	},
	StateDraining: {
		targets: []State{
			StateStopping,
			StateForcing,
		},
	},
	StateStopping: {
		targets: []State{
			StateStopped,
			StateForcing,
		},
	},
	StateForcing: {
		targets: []State{StateStopped},
	},
	StateStopped: {},
}

type Snapshot struct {
	State     State
	Accepting bool
	Ready     bool
	Inflight  int64
	Idle      bool
}

// Controller owns the versiond host lifecycle and admission lease. The lease
// spans the complete proxied response, including the lifetime of an SSE stream.
type Controller struct {
	mu       sync.Mutex
	state    State
	inflight int64
	idle     chan struct{}
}

func NewController() *Controller {
	idle := make(chan struct{})
	close(idle)
	return &Controller{
		state: StateStarting,
		idle:  idle,
	}
}

func (c *Controller) Transition(next State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !validTransition(c.state, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, c.state, next)
	}
	c.state = next
	return nil
}

func validTransition(from, to State) bool {
	spec, fromKnown := stateTable[from]
	_, toKnown := stateTable[to]
	if !fromKnown || !toKnown {
		return false
	}
	if from == to {
		return true
	}
	for _, target := range spec.targets {
		if target == to {
			return true
		}
	}
	return false
}

func acceptsWork(state State) bool {
	spec, ok := stateTable[state]
	return ok && spec.accepting
}

func advertisesReady(state State) bool {
	spec, ok := stateTable[state]
	return ok && spec.ready
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *Controller) snapshotLocked() Snapshot {
	return Snapshot{
		State:     c.state,
		Accepting: acceptsWork(c.state),
		Ready:     advertisesReady(c.state),
		Inflight:  c.inflight,
		Idle:      c.inflight == 0,
	}
}

func (c *Controller) WaitIdle(ctx context.Context) error {
	c.mu.Lock()
	idle := c.idle
	c.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Admission rejects new proxy work unless the host is serving. Lifecycle
// endpoints must be registered outside this middleware so operators can still
// observe a draining host.
func (c *Controller) Admission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.acquire() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "versiond host is not accepting new work", http.StatusServiceUnavailable)
			return
		}
		defer c.release()
		next.ServeHTTP(w, r)
	})
}

func (c *Controller) acquire() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !acceptsWork(c.state) {
		return false
	}
	if c.inflight == 0 {
		c.idle = make(chan struct{})
	}
	c.inflight++
	return true
}

func (c *Controller) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight == 0 {
		return
	}
	c.inflight--
	if c.inflight == 0 {
		close(c.idle)
	}
}
