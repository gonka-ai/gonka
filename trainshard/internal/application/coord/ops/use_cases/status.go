package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type StatusUseCase struct {
	chain shard.ChainReader
	hosts run.HostCommands
}

func NewStatusUseCase(chain shard.ChainReader, hosts run.HostCommands) *StatusUseCase {
	return &StatusUseCase{chain: chain, hosts: hosts}
}

func (uc *StatusUseCase) Execute(ctx context.Context, cmd RunCommand) ([]run.NodeStatus, error) {
	// 1. Load nodes from chain
	record, _, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}

	// 2. One call per host; return collected results
	return run.PerHost(ctx, record.Refs(), run.FailedStatus, func(ctx context.Context, participant vo.Participant, nodes []vo.NodeRef) ([]run.NodeStatus, error) {
		return uc.hosts.Status(ctx, participant, cmd.hostCommand(nodes))
	}), nil
}
