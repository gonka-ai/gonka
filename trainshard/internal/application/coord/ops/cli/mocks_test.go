package cli_test

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type chainStub struct{ nodes []vo.NodeRef }

func (chainStub) Height(context.Context) (vo.Height, error) { return 500, nil }

func (c chainStub) Shard(_ context.Context, id vo.ShardID) (shard.Shard, bool, error) {
	reserved := make([]shard.ReservedNode, 0, len(c.nodes))
	for _, node := range c.nodes {
		reserved = append(reserved, shard.ReservedNode{Ref: node})
	}
	return shard.Shard{ID: id, Status: shard.StatusActive, ExpiresAtHeight: 1000, Nodes: reserved}, true, nil
}

func (chainStub) Reservation(context.Context, vo.NodeRef) (vo.ShardID, bool, error) {
	return 0, false, nil
}

func (chainStub) ActiveShards(context.Context) ([]shard.Shard, error) { return nil, nil }

func (chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{}, nil
}

type hostsStub struct{ statuses map[vo.NodeRef]run.NodeStatus }

func (h hostsStub) Deploy(context.Context, vo.Host, run.DeployCall) ([]run.NodeResult, error) {
	return nil, nil
}

func (h hostsStub) Start(context.Context, vo.Host, run.HostCommand) ([]run.NodeResult, error) {
	return nil, nil
}

func (h hostsStub) Stop(context.Context, vo.Host, run.StopCall) ([]run.NodeResult, error) {
	return nil, nil
}

func (h hostsStub) Status(_ context.Context, _ vo.Host, call run.HostCommand) ([]run.NodeStatus, error) {
	statuses := make([]run.NodeStatus, 0, len(call.Nodes))
	for _, node := range call.Nodes {
		statuses = append(statuses, h.statuses[node])
	}
	return statuses, nil
}
