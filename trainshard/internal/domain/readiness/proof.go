package readiness

import (
	"context"
	"errors"
	"sync"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
)

// ProofKeeps is how long an answer stands. The runtime check costs a container, and asking at
// every refresh loads the engine it judges
const ProofKeeps = 10 * time.Minute

type Proof struct{ at time.Time }

func (p Proof) Holds(now time.Time) bool {
	return !p.at.IsZero() && now.Sub(p.at) < ProofKeeps
}

// After keeps a standing proof through ErrUnavailable: a busy engine has not answered no
func (p Proof) After(now time.Time, err error) (Proof, error) {
	switch {
	case err == nil:
		return Proof{at: now}, nil
	case errors.Is(err, shared.ErrUnavailable) && !p.at.IsZero():
		return p, nil
	default:
		return Proof{}, err
	}
}

type Prover struct {
	probe ports.Probe
	clock ports.Clock
	mu    sync.Mutex
	proof Proof
}

func NewProver(probe ports.Probe, clock ports.Clock) *Prover {
	return &Prover{probe: probe, clock: clock}
}

// The engine is asked under the lock because the check's container has a fixed name
func (p *Prover) GPUContainer(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.clock.Now()
	if p.proof.Holds(now) {
		return nil
	}

	proof, err := p.proof.After(now, p.probe.GPUContainer(ctx))
	p.proof = proof
	return err
}

func (p *Prover) FreeDiskBytes(ctx context.Context) (int64, error) { return p.probe.FreeDiskBytes(ctx) }

func (p *Prover) MeshPortReachable(ctx context.Context) error { return p.probe.MeshPortReachable(ctx) }
