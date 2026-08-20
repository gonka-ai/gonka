package mesh

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// Network host mesh, interface lives in the run netns
type Network interface {
	// Identity returns the member; never rotates the key
	Identity(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (Member, error)
	// Apply brings the interface up with the peer list
	Apply(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, peers []Peer) error
	// Present returns whether key and interface exist
	Present(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (key bool, up bool, err error)
	// Reach returns whether this node can see the peer
	Reach(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, peer Peer) (bool, error)
	// Remove drops key, interface, peer list; ok if already gone
	Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Shards returns every shard this node still holds a key for
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
}

// Hosts coordinator mesh calls to hosts
type Hosts interface {
	// Identities returns signed members; skips nodes with no key
	Identities(ctx context.Context, shardID vo.ShardID, participant vo.Participant) ([]Identity, error)
	// Apply hands one node its signed peer list
	Apply(ctx context.Context, cfg Config, node vo.NodeRef) error
	// Probe returns pairs this node can't see
	Probe(ctx context.Context, cfg Config, node vo.NodeRef) ([]Pair, error)
}
