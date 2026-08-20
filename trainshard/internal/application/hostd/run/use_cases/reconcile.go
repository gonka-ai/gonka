package usecases

import (
	"context"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
)

type ReconcileUseCase struct {
	chain    run.Reservations
	runs     run.RunStore
	machine  run.Machine
	clock    ports.Clock
	patience time.Duration
	applying syncx.Keyed[vo.NodeRef]
}

func NewReconcileUseCase(chain run.Reservations, runs run.RunStore, machine run.Machine, clock ports.Clock, patience time.Duration) *ReconcileUseCase {
	return &ReconcileUseCase{chain: chain, runs: runs, machine: machine, clock: clock, patience: patience}
}

func (uc *ReconcileUseCase) Execute(ctx context.Context, node vo.NodeRef) error {
	// 1. Hold the node; the ticker and every host command apply through here
	defer uc.applying.Lock(node)()

	return uc.converge(ctx, node)
}

// Record writes what the node should hold and converges it without letting the node go in
// between, so the ticker never applies a command that is only half written
func (uc *ReconcileUseCase) Record(ctx context.Context, node vo.NodeRef, write func(context.Context) error) error {
	defer uc.applying.Lock(node)()

	if err := write(ctx); err != nil {
		return err
	}
	return uc.converge(ctx, node)
}

func (uc *ReconcileUseCase) converge(ctx context.Context, node vo.NodeRef) error {
	// 2. Load what the machine was last told to hold
	state, _, err := uc.runs.Load(ctx, node)
	if err != nil {
		return err
	}

	// 3. Ask the chain and the mesh what this node owes now
	desired, err := run.ReadDesired(ctx, uc.chain, uc.machine.Mesh, node, state)
	if err != nil {
		return err
	}

	// 4. Record the shard before touching the machine
	now := uc.clock.Now()
	if desired.Reserved && state.Reserve(desired.Shard, now) {
		if err := run.RecordReservation(ctx, uc.runs, node, desired.Shard, now); err != nil {
			return err
		}
	}

	// 5. Observe the machine
	observed, err := uc.machine.Observe(ctx, node, desired)
	if err != nil {
		return err
	}

	// 6. Hand back a node that is out of time; cleanup runs on the next pass
	if reason, kick := run.Autokick(desired, observed, state, now, uc.patience); kick {
		return uc.chain.Release(ctx, desired.Shard, node, reason)
	}

	// 7. Wipe what a shard this node no longer serves left behind, before it can be handed back
	if err := uc.machine.Sweep(ctx, node, desired.Shard); err != nil {
		return err
	}

	// 8. Apply the plan, stop on first error
	for _, action := range run.Plan(desired, observed) {
		if err := uc.machine.Apply(ctx, node, desired, action); err != nil {
			return run.RecordFault(ctx, uc.runs, node, action, err, now)
		}
	}

	// 9. Clear a fault the plan already solved
	if state.Fault != nil {
		return run.ClearFault(ctx, uc.runs, node)
	}
	return nil
}
