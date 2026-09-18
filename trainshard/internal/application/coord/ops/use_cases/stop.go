package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type StopUseCase struct {
	chain shard.ChainReader
	hosts run.HostCommands
}

func NewStopUseCase(chain shard.ChainReader, hosts run.HostCommands) *StopUseCase {
	return &StopUseCase{chain: chain, hosts: hosts}
}

func (uc *StopUseCase) Execute(ctx context.Context, cmd StopCommand) ([]run.NodeResult, error) {
	// 1. Load nodes from chain, refusing a shard that is already over
	record, err := shard.ReadActive(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}

	// 2. One call per host; return collected results
	return run.PerHost(ctx, record.Hosts(), run.Failed, func(ctx context.Context, host vo.Host) ([]run.NodeResult, error) {
		return uc.hosts.Stop(ctx, host, run.StopCall{HostCommand: cmd.hostCommand(host.Nodes), Grace: cmd.Grace})
	}), nil
}
