package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type StopUseCase struct {
	chain      shard.ChainReader
	runs       run.RunStore
	once       *run.Once
	containers run.Containers
	converge   *run.Converger
	clock      ports.Clock
}

func NewStopUseCase(
	chain shard.ChainReader,
	runs run.RunStore,
	once *run.Once,
	containers run.Containers,
	converge *run.Converger,
	clock ports.Clock,
) *StopUseCase {
	return &StopUseCase{chain: chain, runs: runs, once: once, containers: containers, converge: converge, clock: clock}
}

func (uc *StopUseCase) Execute(ctx context.Context, cmd StopCommand) ([]run.NodeResult, error) {
	// 1. Read the shard from chain
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}
	if err := shard.CanAsk(cmd.Shard, cmd.Actor, record); err != nil {
		return nil, err
	}

	// 2. Answer once per request: refuse, mark should-stop and converge each node under its lock
	return uc.once.Do(ctx, cmd.request(run.OpStop), func(ctx context.Context) []run.NodeResult {
		return run.PerNode(cmd.Nodes, run.Failed, func(node vo.NodeRef) (run.NodeResult, error) {
			if err := shard.CanApply(cmd.forNode(node), record, uc.clock.Now(), height); err != nil {
				return run.NodeResult{}, err
			}
			write := func(ctx context.Context) error {
				container, err := uc.containers.Inspect(ctx, cmd.Shard, node)
				if err != nil {
					return err
				}
				if err := run.CanStop(container.State); err != nil {
					return err
				}
				return run.RecordStop(ctx, uc.runs, node, cmd.Grace)
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
