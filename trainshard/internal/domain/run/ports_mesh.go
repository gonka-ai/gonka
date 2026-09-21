package run

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// RunNetwork is the run's mesh as the run needs it
type RunNetwork interface {
	// Create makes the key and publishes the member; same key if called again
	Create(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Identified returns whether the signed member is stored for the coordinator to collect;
	// false if none, not an error
	Identified(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// Configured returns whether a peer list was accepted
	Configured(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// Present returns whether the key exists and whether the interface is up with the accepted
	// peer list; an interface holding an older list is not up. Absent is false, not an error
	Present(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (key bool, up bool, err error)
	// Placement returns this node's rank on the mesh; errors until a peer list was accepted
	Placement(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (vo.Placement, error)
	// Apply brings the interface up from the accepted list
	Apply(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Remove drops key, interface, peer list; ok if already gone
	Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Shards returns every shard this node still holds a mesh key for
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
}
