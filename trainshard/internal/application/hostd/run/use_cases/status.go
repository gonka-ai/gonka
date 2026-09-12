package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type StatusUseCase struct {
	chain   shard.ChainReader
	runs    run.RunStore
	machine run.Machine
	clock   ports.Clock
}

func NewStatusUseCase(
	chain shard.ChainReader,
	runs run.RunStore,
	machine run.Machine,
	clock ports.Clock,
) *StatusUseCase {
	return &StatusUseCase{chain: chain, runs: runs, machine: machine, clock: clock}
}

func (uc *StatusUseCase) Execute(ctx context.Context, cmd NodesCommand) ([]run.NodeStatus, error) {
	// 1. Authorize the read
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}

	// 2. Take from the record only what a run needs from it
	reservation := run.Reservation{Shard: record.ID, BaseImage: record.BaseImage, Active: record.IsActive(height)}

	// 3. Return what the machine holds
	return run.PerNode(cmd.Nodes, run.FailedStatus, func(node vo.NodeRef) (run.NodeStatus, error) {
		if err := shard.CanObserve(cmd.forNode(node), record, height); err != nil {
			return run.NodeStatus{}, err
		}
		state, _, err := uc.runs.Load(ctx, node)
		if err != nil {
			return run.NodeStatus{}, err
		}
		configured, err := uc.machine.Mesh.Configured(ctx, record.ID, node)
		if err != nil {
			return run.NodeStatus{}, err
		}
		desired := run.DesiredFor(reservation, state, configured)
		observed, err := uc.machine.Observe(ctx, node, desired)
		if err != nil {
			return run.NodeStatus{}, err
		}
		return run.StatusOf(node, desired, observed, state.Fault), nil
	}), nil
}
