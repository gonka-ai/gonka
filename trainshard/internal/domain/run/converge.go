package run

import (
	"context"
	"time"

	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
)

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

func (c *Converger) Converge(ctx context.Context, node vo.NodeRef) error {
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
	return c.converge(ctx, node)
}

func (c *Converger) converge(ctx context.Context, node vo.NodeRef) error {
	// 1. Load what the machine was last told to hold
	state, _, err := c.runs.Load(ctx, node)
	if err != nil {
		return err
	}

	// 2. Ask the chain and the mesh what this node owes now
	desired, err := ReadDesired(ctx, c.chain, c.machine.Mesh, node, state)
	if err != nil {
		return err
	}

	// 3. Record the shard before touching the machine
	now := c.clock.Now()
	if desired.Reserved && state.Reserve(desired.Shard, now) {
		if err := RecordReservation(ctx, c.runs, node, desired.Shard, now); err != nil {
			return err
		}
	}

	// 4. Observe the machine
	observed, err := c.machine.Observe(ctx, node, desired)
	if err != nil {
		return err
	}

	// 5. Note whether the node is ready, so a node that slips mid-run is given the same wait a
	// fresh one gets rather than none at all
	if err := TrackPreparedness(ctx, c.runs, node, &state, desired, observed, now); err != nil {
		return err
	}

	// 6. Hand back a node that is out of time; cleanup runs on the next pass
	if reason, kick := Autokick(desired, observed, state, now, c.patience); kick {
		return c.chain.Release(ctx, desired.Shard, node, reason)
	}

	// 7. Wipe what a shard this node no longer serves left behind, before it can be handed back
	if err := c.machine.Sweep(ctx, node, desired.Shard); err != nil {
		return err
	}

	// 8. Apply the plan, stop on first error
	for _, action := range Plan(desired, observed) {
		if err := c.machine.Apply(ctx, node, desired, action); err != nil {
			return RecordFault(ctx, c.runs, node, action, err, now)
		}
	}

	// 9. Clear a fault the plan already solved
	if state.Fault != nil {
		return ClearFault(ctx, c.runs, node)
	}
	return nil
}
