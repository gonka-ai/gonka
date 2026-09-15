package shard

import (
	"context"
	"time"

	"trainshard/internal/domain/shared/vo"
)

// ChainReader what's true on chain; events only wake us
type ChainReader interface {
	// Height returns current height
	Height(ctx context.Context) (vo.Height, error)
	// Shard returns the record, or none
	Shard(ctx context.Context, shardID vo.ShardID) (shard Shard, found bool, err error)
	// Reservation returns this node's shard, or none
	Reservation(ctx context.Context, node vo.NodeRef) (shardID vo.ShardID, found bool, err error)
	// ActiveShards returns every open shard
	ActiveShards(ctx context.Context) ([]Shard, error)
	// Hardware returns what the node claims on chain
	Hardware(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
}

// ChainWatcher hint only, not truth
type ChainWatcher interface {
	// Watch returns a chan that pings when state may have changed
	Watch(ctx context.Context) (<-chan struct{}, error)
}

// ChainSubmitter host txs through the dAPI
type ChainSubmitter interface {
	// OptIn offers the node until TTL
	OptIn(ctx context.Context, node vo.NodeRef, ttl time.Duration) error
	// Release gives the reservation back
	Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error
}

// ChainLifecycle is what only the shard's own creator may ask: where a run begins and where it ends
type ChainLifecycle interface {
	// Assemble turns a passed proposal into a shard that holds its nodes
	Assemble(ctx context.Context, proposal uint64) (vo.ShardID, error)
	// Settle closes the run and hands every node back
	Settle(ctx context.Context, shardID vo.ShardID) error
}
