// Package fake is a chain for tests and nothing else. No binary wires it: a daemon without a chain
// reserves whatever it is asked for, so a missing one stops it at startup instead
package fake

import (
	"context"
	"slices"
	"sync"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type Chain struct {
	mu           sync.RWMutex
	height       vo.Height
	shards       map[vo.ShardID]shard.Shard
	reservations map[vo.NodeRef]vo.ShardID
	hardware     map[vo.NodeRef]vo.GPUInventory
	events       chan struct{}
}

func newChain() *Chain {
	return &Chain{
		shards:       map[vo.ShardID]shard.Shard{},
		reservations: map[vo.NodeRef]vo.ShardID{},
		hardware:     map[vo.NodeRef]vo.GPUInventory{},
		events:       make(chan struct{}, 1),
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

func (c *Chain) Watch(context.Context) (<-chan struct{}, error) {
	return c.events, nil
}

func (c *Chain) Release(_ context.Context, shardID vo.ShardID, node vo.NodeRef, _ vo.ReleaseReason) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.reservations, node)
	if record, found := c.shards[shardID]; found {
		record.Nodes = slices.DeleteFunc(record.Nodes, func(reserved shard.ReservedNode) bool { return reserved.Ref == node })
		c.shards[shardID] = record
	}
	c.hint()
	return nil
}

func (c *Chain) hint() {
	select {
	case c.events <- struct{}{}:
	default:
	}
}
