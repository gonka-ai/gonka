package shard

import (
	"context"
	"time"

	"trainshard/internal/domain/shared/vo"
)

// ChainReader is what is true on chain, read fresh on every call; an event is only a hint to read
// again. None found is false, not an error; an error means the chain could not be read
type ChainReader interface {
	// Height returns the current block height
	Height(ctx context.Context) (vo.Height, error)
	// Shard returns the shard record, with only the nodes still active in it
	Shard(ctx context.Context, shardID vo.ShardID) (shard Shard, found bool, err error)
	// Reservation returns the shard that holds the node
	Reservation(ctx context.Context, node vo.NodeRef) (shardID vo.ShardID, found bool, err error)
	// ActiveShards returns every shard the chain still lists as active, which may include one past
	// its expiry height that the chain has not closed yet
	ActiveShards(ctx context.Context) ([]Shard, error)
	// Hardware returns the gpus the node declares on chain; none declared is a zero inventory
	Hardware(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
}

// ChainWatcher wakes a loop early; a missed or spurious hint changes nothing, since the loop reads
// the chain again either way
type ChainWatcher interface {
	// Watch returns a channel that pings when the chain may have changed, closed when ctx ends
	Watch(ctx context.Context) (<-chan struct{}, error)
}

// ChainSubmitter is the host's transactions, signed by the dAPI with the operational key. Nil means
// the chain accepted the transaction; what it did is read back through ChainReader
type ChainSubmitter interface {
	// OptIn offers the node until TTL and publishes where this daemon answers for it; a repeat
	// moves the expiry forward and replaces the address
	OptIn(ctx context.Context, node vo.NodeRef, ttl time.Duration) error
	// Release gives the node's reservation in the shard back; a repeat with the same reason is a
	// no-op on chain
	Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error
}

// ChainLifecycle is what only the shard's own creator may ask: where a run begins and where it ends
type ChainLifecycle interface {
	// Assemble turns a passed proposal into a shard that holds its nodes, and returns the shard id
	// the chain gave it
	Assemble(ctx context.Context, proposal uint64) (vo.ShardID, error)
	// Settle closes the run and hands every node back
	Settle(ctx context.Context, shardID vo.ShardID) error
}
