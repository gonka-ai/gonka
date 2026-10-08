package mesh

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// Network is the host side of the mesh: one key per shard and node, and an interface that lives in
// the run's network namespace, never on the host
type Network interface {
	// Identity returns this node's member, creating its key on the first call; a repeat returns the
	// same key, which is never rotated. Errors for a node this host does not serve or no endpoint
	Identity(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (Member, error)
	// Apply brings the interface up with this node's own address and exactly these peers, replacing
	// the list it held; safe to repeat. A failed create leaves nothing on the host
	Apply(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, peers []Peer) error
	// Present returns whether the key exists and whether the interface is up holding exactly
	// these peers and this node's own address; with no peers given, whether it exists at all.
	// Absent is false, not an error
	Present(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, peers []Peer) (key bool, up bool, err error)
	// Reach returns whether this node shook hands with the peer recently; not reached, or a peer
	// that cannot be one, is false, not an error. An error means the interface could not be asked
	Reach(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, peer Peer) (bool, error)
	// Remove drops the interface, the namespace that holds it and the key; ok if already gone
	Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Shards returns every shard this node still holds a key for, so cleanup finds them after a restart
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
	// Interface names the mesh link this node's run sees; errors for a node this host does not serve
	Interface(node vo.NodeRef) (string, error)
}

// Hosts is the coordinator's mesh calls to the machine that serves a node. An error means the host
// did not answer or refused; what that costs the node is the caller's decision
type Hosts interface {
	// Identities returns the signed members the host published for the shard, leaving out a node
	// with no key yet. They are the host's claim, and the caller verifies them
	Identities(ctx context.Context, shardID vo.ShardID, host vo.Host) ([]Identity, error)
	// Apply hands one node its signed peer list; the same list again changes nothing. An error
	// means the node did not take it
	Apply(ctx context.Context, cfg Config, host vo.Host, node vo.NodeRef) error
	// Probe returns the pairs this node cannot see, each with this node on one end
	Probe(ctx context.Context, cfg Config, host vo.Host, node vo.NodeRef) ([]Pair, error)
}
