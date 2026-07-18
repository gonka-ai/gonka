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
	StateStarting State = "starting"
	StateServing  State = "serving"
	StateDraining State = "draining"
	StateStopping State = "stopping"
	StateForcing  State = "forcing"
	StateStopped  State = "stopped"
)

var ErrInvalidTransition = errors.New("invalid versiond host state transition")

type Snapshot struct {
	State     State
	Accepting bool
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
	if c.state == next {
		return nil
	}
	if !validTransition(c.state, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, c.state, next)
	}
	c.state = next
	return nil
}

func validTransition(from, to State) bool {
	switch from {
	case StateStarting:
		return to == StateServing || to == StateDraining || to == StateForcing
	case StateServing:
		return to == StateDraining || to == StateForcing
	case StateDraining:
		return to == StateStopping || to == StateForcing
	case StateStopping:
		return to == StateStopped || to == StateForcing
	case StateForcing:
		return to == StateStopped
	case StateStopped:
		return false
	default:
		return false
	}
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *Controller) snapshotLocked() Snapshot {
	return Snapshot{
		State:     c.state,
		Accepting: c.state == StateServing,
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
	if c.state != StateServing {
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
