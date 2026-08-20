package fake

import (
	"context"
	"slices"
	"sync"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type Chain struct {
	mu           sync.RWMutex
	height       vo.Height
	shards       map[vo.ShardID]shard.Shard
	reservations map[vo.NodeRef]vo.ShardID
	hardware     map[vo.NodeRef]vo.GPUInventory
	optIns       map[vo.NodeRef]time.Time
	releases     []Release
	events       chan struct{}
	clock        ports.Clock
}

type Release struct {
	Shard  vo.ShardID
	Node   vo.NodeRef
	Reason vo.ReleaseReason
}

func New(clock ports.Clock) *Chain {
	return &Chain{
		shards:       map[vo.ShardID]shard.Shard{},
		reservations: map[vo.NodeRef]vo.ShardID{},
		hardware:     map[vo.NodeRef]vo.GPUInventory{},
		optIns:       map[vo.NodeRef]time.Time{},
		events:       make(chan struct{}, 1),
		clock:        clock,
	}
}

func (c *Chain) Height(context.Context) (vo.Height, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.height, nil
}

func (c *Chain) Shard(_ context.Context, id vo.ShardID) (shard.Shard, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	record, found := c.shards[id]
	return record, found, nil
}

func (c *Chain) Reservation(_ context.Context, node vo.NodeRef) (vo.ShardID, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	id, found := c.reservations[node]
	return id, found, nil
}

func (c *Chain) Reserved(_ context.Context, node vo.NodeRef) (run.Reservation, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	id, found := c.reservations[node]
	if !found {
		return run.Reservation{}, false, nil
	}
	record, found := c.shards[id]
	if !found {
		return run.Reservation{}, false, nil
	}
	return run.Reservation{Shard: id, BaseImage: record.BaseImage, Active: record.IsActive(c.height)}, true, nil
}

func (c *Chain) ActiveShards(context.Context) ([]shard.Shard, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	active := make([]shard.Shard, 0, len(c.shards))
	for _, record := range c.shards {
		if record.IsActive(c.height) {
			active = append(active, record)
		}
	}
	return active, nil
}

func (c *Chain) Hardware(_ context.Context, node vo.NodeRef) (vo.GPUInventory, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hardware[node], nil
}

func (c *Chain) Watch(ctx context.Context) (<-chan struct{}, error) {
	return c.events, nil
}

func (c *Chain) OptIn(_ context.Context, node vo.NodeRef, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.optIns[node] = c.clock.Now().Add(ttl)
	return nil
}

func (c *Chain) Release(_ context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.releases = append(c.releases, Release{Shard: shardID, Node: node, Reason: reason})
	delete(c.reservations, node)
	if record, found := c.shards[shardID]; found {
		record.Nodes = slices.DeleteFunc(record.Nodes, func(reserved shard.ReservedNode) bool { return reserved.Ref == node })
		c.shards[shardID] = record
	}
	c.hint()
	return nil
}

func (c *Chain) Releases() []Release {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Release(nil), c.releases...)
}

func (c *Chain) SetHeight(height vo.Height) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.height = height
	c.hint()
}

func (c *Chain) hint() {
	select {
	case c.events <- struct{}{}:
	default:
	}
}
