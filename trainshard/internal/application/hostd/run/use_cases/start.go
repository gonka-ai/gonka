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
	once       *run.Once
	containers run.Containers
	converge   *run.Converger
	clock      ports.Clock
}

func NewStartUseCase(
	chain shard.ChainReader,
	runs run.RunStore,
	once *run.Once,
	containers run.Containers,
	converge *run.Converger,
	clock ports.Clock,
) *StartUseCase {
	return &StartUseCase{chain: chain, runs: runs, once: once, containers: containers, converge: converge, clock: clock}
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

	// 2. Answer once per request: refuse, mark should-run and converge each node under its lock
	return uc.once.Do(ctx, cmd.request(run.OpStart), func(ctx context.Context) []run.NodeResult {
		return run.PerNode(cmd.Nodes, run.Failed, func(node vo.NodeRef) (run.NodeResult, error) {
			if err := shard.CanApply(cmd.forNode(node), record, uc.clock.Now(), height); err != nil {
				return run.NodeResult{}, err
			}
			write := func(ctx context.Context) error {
				container, err := uc.containers.Inspect(ctx, cmd.Shard, node)
				if err != nil {
					return err
				}
				if err := run.CanStart(container.State); err != nil {
					return err
				}
				return run.RecordStart(ctx, uc.runs, node)
			}
			if err := uc.converge.Record(ctx, node, write); err != nil {
				return run.NodeResult{}, err
			}
			applied, err := uc.containers.Inspect(ctx, cmd.Shard, node)
			if err != nil {
				return run.NodeResult{}, err
			}
			return run.ResultOf(node, applied), nil
		})
	})
}
