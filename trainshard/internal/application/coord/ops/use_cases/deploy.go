package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type DeployUseCase struct {
	chain shard.ChainReader
	hosts run.HostCommands
}

func NewDeployUseCase(chain shard.ChainReader, hosts run.HostCommands) *DeployUseCase {
	return &DeployUseCase{chain: chain, hosts: hosts}
}

func (uc *DeployUseCase) Execute(ctx context.Context, cmd DeployCommand) ([]run.NodeResult, error) {
	// 1. Load nodes from chain, refusing a shard that is already over
	record, err := shard.ReadActive(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}

	// 2. One call per host; return collected results
	return run.PerHost(ctx, record.Hosts(), run.Failed, func(ctx context.Context, host vo.Host) ([]run.NodeResult, error) {
		return uc.hosts.Deploy(ctx, host, run.DeployCall{HostCommand: cmd.hostCommand(host.Nodes), Run: cmd.Run})
	}), nil
}
