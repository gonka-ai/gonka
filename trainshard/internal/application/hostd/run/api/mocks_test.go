package api_test

import (
	"context"
	"time"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type chainStub struct{}

func (chainStub) Height(context.Context) (vo.Height, error) { return 500, nil }

func (chainStub) Shard(context.Context, vo.ShardID) (shard.Shard, bool, error) {
	return shard.Shard{}, false, nil
}

func (chainStub) Reservation(context.Context, vo.NodeRef) (vo.ShardID, bool, error) {
	return 7, true, nil
}

func (chainStub) ActiveShards(context.Context) ([]shard.Shard, error) { return nil, nil }

func (chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{}, nil
}

type submitterStub struct{ released []vo.NodeRef }

func (s *submitterStub) OptIn(context.Context, vo.NodeRef, time.Duration) error { return nil }

func (s *submitterStub) Release(_ context.Context, _ vo.ShardID, node vo.NodeRef, _ vo.ReleaseReason) error {
	s.released = append(s.released, node)
	return nil
}
