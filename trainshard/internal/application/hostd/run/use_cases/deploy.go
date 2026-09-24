package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type DeployUseCase struct {
	chain      shard.ChainReader
	runs       run.RunStore
	once       *run.Once
	containers run.Containers
	converge   *run.Converger
	clock      ports.Clock
	limits     run.Limits
}

func NewDeployUseCase(
	chain shard.ChainReader,
	runs run.RunStore,
	once *run.Once,
	containers run.Containers,
	converge *run.Converger,
	clock ports.Clock,
	limits run.Limits,
) *DeployUseCase {
	return &DeployUseCase{
		chain:      chain,
		runs:       runs,
		once:       once,
		containers: containers,
		converge:   converge,
		clock:      clock,
		limits:     limits,
	}
}

func (uc *DeployUseCase) Execute(ctx context.Context, cmd DeployCommand) ([]run.NodeResult, error) {
	// 1. Read the shard from chain
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}
	if err := shard.CanAsk(cmd.Shard, cmd.Actor, record); err != nil {
		return nil, err
	}

	// 2. Answer once per request: refuse, record and converge each node under its lock
	return uc.once.Do(ctx, cmd.request(run.OpDeploy), func(ctx context.Context) []run.NodeResult {
		return run.PerNode(cmd.Nodes, run.Failed, func(node vo.NodeRef) (run.NodeResult, error) {
			if err := shard.CanApply(cmd.forNode(node), record, uc.clock.Now(), height); err != nil {
				return run.NodeResult{}, err
			}
			var before run.RunState
			write := func(ctx context.Context) error {
				container, err := uc.containers.Inspect(ctx, cmd.Shard, node)
				if err != nil {
					return err
				}
				if err := run.CanDeploy(cmd.Run, uc.limits, container.State); err != nil {
					return err
				}
				if before, _, err = uc.runs.Load(ctx, node); err != nil {
					return err
				}
				return run.RecordDeploy(ctx, uc.runs, node, cmd.Shard, cmd.Run)
			}
			undo := func(ctx context.Context) error { return run.UndoDeploy(ctx, uc.runs, node, before) }
			if err := uc.converge.Attempt(ctx, node, write, undo); err != nil {
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
