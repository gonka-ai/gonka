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
	return run.PerHost(ctx, record.Refs(), run.Failed, func(ctx context.Context, participant vo.Participant, nodes []vo.NodeRef) ([]run.NodeResult, error) {
		return uc.hosts.Stop(ctx, participant, run.StopCall{HostCommand: cmd.hostCommand(nodes), Grace: cmd.Grace})
	}), nil
}
