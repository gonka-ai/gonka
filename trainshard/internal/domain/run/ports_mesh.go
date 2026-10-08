package run

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// RunNetwork is the run's mesh as the run needs it, one per shard and node. Absent is false, not an
// error; an error means the mesh could not be read or changed
type RunNetwork interface {
	// Create makes the run's key and stores the signed member; a repeat keeps the same key and
	// finishes a member that was not stored
	Create(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Identified returns whether the signed member is stored for the coordinator to collect
	Identified(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// Configured returns whether a peer list was accepted
	Configured(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// Present returns whether the key exists and whether the interface is up with the accepted
	// peer list; an interface holding an older list is not up
	Present(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (key bool, up bool, err error)
	// Silent returns the peers on the accepted list this node has not heard from recently; an
	// interface that is up says nothing about whether its links still carry anything
	Silent(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) ([]vo.NodeRef, error)
	// Placement returns this node's rank on the mesh; an error until a peer list was accepted
	Placement(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (vo.Placement, error)
	// Apply brings the interface up from the accepted list, an error if none was accepted; a repeat
	// brings up the same list again
	Apply(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Remove drops the key, the interface and the peer list; one already gone is a no-op
	Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Shards returns every shard this node still holds a mesh key for
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
}
