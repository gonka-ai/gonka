package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type StartUseCase struct {
	chain      shard.ChainReader
	runs       run.RunStore
	log        run.RequestLog
	containers run.Containers
	reconcile  Reconciler
	clock      ports.Clock
}

func NewStartUseCase(
	chain shard.ChainReader,
	runs run.RunStore,
	log run.RequestLog,
	containers run.Containers,
	reconcile Reconciler,
	clock ports.Clock,
) *StartUseCase {
	return &StartUseCase{chain: chain, runs: runs, log: log, containers: containers, reconcile: reconcile, clock: clock}
}

func (uc *StartUseCase) Execute(ctx context.Context, cmd NodesCommand) ([]run.NodeResult, error) {
	// 1. Read the shard from chain
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}
	if err := shard.CanAsk(cmd.Shard, cmd.Actor, record); err != nil {
		return nil, err
	}

	// 2. Replay stored answer if seen
	recorded, found, err := uc.log.Result(ctx, cmd.RequestID)
	if err != nil || found {
		return recorded, err
	}

	// 3. Refuse, mark should-run and converge each node
	results := run.PerNode(cmd.Nodes, run.Failed, func(node vo.NodeRef) (run.NodeResult, error) {
		container, err := uc.containers.Inspect(ctx, cmd.Shard, node)
		if err != nil {
			return run.NodeResult{}, err
		}
		if err := shard.CanApply(cmd.forNode(node), record, uc.clock.Now(), height); err != nil {
			return run.NodeResult{}, err
		}
		if err := run.CanStart(container.State); err != nil {
			return run.NodeResult{}, err
		}
		if err := run.RecordStart(ctx, uc.runs, node); err != nil {
			return run.NodeResult{}, err
		}
		if err := uc.reconcile.Execute(ctx, node); err != nil {
			return run.NodeResult{}, err
		}
		applied, err := uc.containers.Inspect(ctx, cmd.Shard, node)
		if err != nil {
			return run.NodeResult{}, err
		}
		return run.ResultOf(node, applied), nil
	})

	// 4. Store the answer by request id
	if err := uc.log.Record(ctx, cmd.RequestID, results); err != nil {
		return nil, err
	}
	return results, nil
}
