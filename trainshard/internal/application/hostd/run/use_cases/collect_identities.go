package usecases

import (
	"context"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type CollectIdentitiesUseCase struct {
	chain shard.ChainReader
	store mesh.Store
	nodes []vo.NodeRef
}

func NewCollectIdentitiesUseCase(chain shard.ChainReader, store mesh.Store, nodes []vo.NodeRef) *CollectIdentitiesUseCase {
	return &CollectIdentitiesUseCase{chain: chain, store: store, nodes: nodes}
}

func (uc *CollectIdentitiesUseCase) Execute(ctx context.Context, shardID vo.ShardID, actor shard.Actor) ([]mesh.Identity, error) {
	// 1. Read the shard from chain
	record, height, err := shard.Read(ctx, uc.chain, shardID)
	if err != nil {
		return nil, err
	}

	// 2. Refuse a closed shard and anyone but its creator or run key
	if !record.IsActive(height) {
		return nil, shard.ErrShardClosed
	}
	if !actor.AuthorizedFor(record) {
		return nil, shard.ErrNotAuthorized
	}

	// 3. Return the identity of every reserved node that has one yet
	identities := make([]mesh.Identity, 0, len(uc.nodes))
	for _, node := range uc.nodes {
		if !record.Reserves(node) {
			continue
		}
		identity, found, err := uc.store.Identity(ctx, shardID, node)
		if err != nil {
			return nil, err
		}
		if found {
			identities = append(identities, identity)
		}
	}
	return identities, nil
}
