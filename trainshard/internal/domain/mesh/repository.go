package mesh

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

type Identity struct {
	Member    Member
	Signature []byte
}

// Store member, host signature, accepted peer list
type Store interface {
	// Identity returns the published member, or none
	Identity(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (identity Identity, found bool, err error)
	// SaveIdentity stores the member and host signature
	SaveIdentity(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, identity Identity) error
	// Config returns the accepted peer list, or none
	Config(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (config Config, found bool, err error)
	// SaveConfig stores the peer list
	SaveConfig(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, config Config) error
	// Forget drops it; ok if already gone
	Forget(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
}
