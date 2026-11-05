package gatewayproto

import (
	"context"
	"sync"
)

// Gate excludes an escrow's finalization and timeout sweep. Copies refer to the
// same gate. Callers choose blocking, context-aware or nonblocking acquisition.
type Gate chan struct{}

func NewGate() Gate { return make(Gate, 1) }

func (g Gate) Lock() { g <- struct{}{} }

func (g Gate) Unlock() {
	select {
	case <-g:
	default:
	}
}

func (g Gate) TryLock() bool {
	select {
	case g <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g Gate) LockContext(ctx context.Context) error {
	select {
	case g <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Gates keeps a stable gate for each escrow. Its zero value is ready for use.
// Gates must not be copied after first use.
type Gates struct {
	mu    sync.Mutex
	gates map[string]Gate
}

func (g *Gates) For(escrowID string) Gate {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates == nil {
		g.gates = make(map[string]Gate)
	}
	gate, ok := g.gates[escrowID]
	if !ok {
		gate = NewGate()
		g.gates[escrowID] = gate
	}
	return gate
}
