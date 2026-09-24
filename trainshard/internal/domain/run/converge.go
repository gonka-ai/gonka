package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
)

type Outcome struct {
	Reserved bool
	Waiting  string
}

// Converger is the only writer of a node's machine: the loop and every host command go through
// it, under one lock per node
type Converger struct {
	chain    Reservations
	runs     RunStore
	machine  Machine
	clock    ports.Clock
	patience time.Duration
	applying syncx.Keyed[vo.NodeRef]
}

func NewConverger(chain Reservations, runs RunStore, machine Machine, clock ports.Clock, patience time.Duration) *Converger {
	return &Converger{chain: chain, runs: runs, machine: machine, clock: clock, patience: patience}
}

func (c *Converger) Converge(ctx context.Context, node vo.NodeRef) (Outcome, error) {
	defer c.applying.Lock(node)()

	return c.converge(ctx, node)
}

// Record writes and converges without releasing the node, so the loop never applies a command
// that is only half written
func (c *Converger) Record(ctx context.Context, node vo.NodeRef, write func(context.Context) error) error {
	defer c.applying.Lock(node)()

	if err := write(ctx); err != nil {
		return err
	}
	_, err := c.converge(ctx, node)
	return err
}

// Attempt is Record for a write the host may refuse on its merits: a refusal is taken back
// under the same lock, or it would stay behind failing every pass until the node is handed back,
// long after the caller was told nothing changed. A write that failed for want of time or of an
// engine stays, so the loop carries on where it stopped
func (c *Converger) Attempt(ctx context.Context, node vo.NodeRef, write, undo func(context.Context) error) error {
	defer c.applying.Lock(node)()

	if err := write(ctx); err != nil {
		return err
	}
	_, err := c.converge(ctx, node)
	if err == nil || !errors.Is(err, shared.ErrValidation) {
		return err
	}
	if undoErr := undo(ctx); undoErr != nil {
		return fmt.Errorf("%w (taking it back also failed: %v)", err, undoErr)
	}
	return err
}

func (c *Converger) converge(ctx context.Context, node vo.NodeRef) (Outcome, error) {
	// 1. Load what the machine was last told to hold
	state, _, err := c.runs.Load(ctx, node)
	if err != nil {
		return Outcome{}, err
	}

	// 2. Ask the chain and the mesh what this node owes now
	desired, err := ReadDesired(ctx, c.chain, c.machine.Mesh, node, state)
	if err != nil {
		return Outcome{}, err
	}

	// 3. Record the shard before touching the machine
	now := c.clock.Now()
	if desired.Reserved && state.Reserve(desired.Shard, now) {
		if err := RecordReservation(ctx, c.runs, node, desired.Shard, now); err != nil {
			return Outcome{}, err
		}
	}

	// 4. Observe the machine
	observed, err := c.machine.Observe(ctx, node, desired)
	if err != nil {
		return Outcome{}, err
	}
	found := Outcome{Reserved: desired.Reserved}
	if desired.Reserved {
		found.Waiting = Unprepared(desired, observed)
	}

	// 5. Note whether the node is ready; a node that slips mid-run gets the same wait a fresh one gets
	if err := TrackPreparedness(ctx, c.runs, node, &state, desired, observed, now); err != nil {
		return found, err
	}

	// 6. Hand back a node that is out of time, once; cleanup runs when the chain shows it
	if reason, kick := Autokick(desired, observed, state, now, c.patience); kick {
		if err := c.chain.Release(ctx, desired.Shard, node, reason); err != nil {
			return found, err
		}
		return found, RecordRelease(ctx, c.runs, node, now)
	}

	// 7. Wipe what a shard this node no longer serves left behind, before it can be handed back
	if err := c.machine.Sweep(ctx, node, desired.Shard); err != nil {
		return found, err
	}

	// 8. Apply the plan, stop on first error; a handback forgets the run, so nothing is left to clear
	for _, action := range Plan(desired, observed) {
		if err := c.machine.Apply(ctx, node, desired, action); err != nil {
			return found, RecordFault(ctx, c.runs, node, action, err, now)
		}
		if action.Kind == ActionReturnNode {
			return found, nil
		}
	}

	// 9. Clear a fault the plan already solved
	if state.Fault != nil {
		return found, ClearFault(ctx, c.runs, node)
	}
	return found, nil
}
