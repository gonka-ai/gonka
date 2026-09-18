package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type StartUseCase struct {
	chain shard.ChainReader
	hosts run.HostCommands
}

func NewStartUseCase(chain shard.ChainReader, hosts run.HostCommands) *StartUseCase {
	return &StartUseCase{chain: chain, hosts: hosts}
}

func (uc *StartUseCase) Execute(ctx context.Context, cmd RunCommand) ([]run.NodeResult, error) {
	// 1. Load nodes from chain, refusing a shard that is already over
	record, err := shard.ReadActive(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}

	// 2. Ask every host which image its containers hold
	statuses := run.PerHost(ctx, record.Hosts(), run.FailedStatus, func(ctx context.Context, host vo.Host) ([]run.NodeStatus, error) {
		return uc.hosts.Status(ctx, host, cmd.hostCommand(host.Nodes))
	})

	// 3. Refuse the whole run unless every node is ready to take it; only we see every host
	if err := run.ReadyToStart(statuses); err != nil {
		return nil, err
	}

	// 4. One call per host; return collected results
	return run.PerHost(ctx, record.Hosts(), run.Failed, func(ctx context.Context, host vo.Host) ([]run.NodeResult, error) {
		return uc.hosts.Start(ctx, host, cmd.hostCommand(host.Nodes))
	}), nil
}
