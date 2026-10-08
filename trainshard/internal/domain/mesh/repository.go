package mesh

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

type Identity struct {
	Member    Member
	Signature []byte
}

// Store is the host's record of a node's mesh for one shard: the member it published, the host
// signature over it and the peer list it accepted. It survives a restart
type Store interface {
	// Identity returns the published member and its signature; found is false when none was saved
	Identity(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (identity Identity, found bool, err error)
	// SaveIdentity replaces the member and the host signature; errors for a node not held for the shard
	SaveIdentity(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, identity Identity) error
	// Config returns the accepted peer list; found is false when none was accepted
	Config(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (config Config, found bool, err error)
	// SaveConfig replaces the accepted peer list; errors for a node not held for the shard
	SaveConfig(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, config Config) error
	// Forget drops the member, the signature and the peer list; ok if already gone
	Forget(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
}
