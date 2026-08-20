package mesh

import (
	"context"

	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type Runtime struct {
	Network  Network
	Store    Store
	Attestor ports.Attestor
}

func (r Runtime) Create(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	member, err := r.Network.Identity(ctx, shardID, node)
	if err != nil {
		return err
	}
	signature, err := r.Attestor.Attest(ctx, IdentityPayload(shardID, member))
	if err != nil {
		return err
	}
	return r.Store.SaveIdentity(ctx, shardID, node, Identity{Member: member, Signature: signature})
}

func (r Runtime) Configured(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error) {
	_, found, err := r.Store.Config(ctx, shardID, node)
	return found, err
}

func (r Runtime) Present(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, bool, error) {
	return r.Network.Present(ctx, shardID, node)
}

func (r Runtime) Apply(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	config, found, err := r.Store.Config(ctx, shardID, node)
	if err != nil {
		return err
	}
	if !found {
		return ErrMissingConfig
	}
	return r.Network.Apply(ctx, shardID, node, config.Peers)
}

func (r Runtime) Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error) {
	return r.Network.Shards(ctx, node)
}

func (r Runtime) Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	if err := r.Network.Remove(ctx, shardID, node); err != nil {
		return err
	}
	return r.Store.Forget(ctx, shardID, node)
}
