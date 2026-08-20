package usecases

import (
	"context"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type ProbeMeshUseCase struct {
	chain   shard.ChainReader
	store   mesh.Store
	network mesh.Network
}

func NewProbeMeshUseCase(chain shard.ChainReader, store mesh.Store, network mesh.Network) *ProbeMeshUseCase {
	return &ProbeMeshUseCase{chain: chain, store: store, network: network}
}

func (uc *ProbeMeshUseCase) Execute(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, actor shard.Actor) ([]vo.NodeRef, error) {
	// 1. Authorize the read
	record, height, err := shard.Read(ctx, uc.chain, shardID)
	if err != nil {
		return nil, err
	}
	if err := shard.CanObserve(shard.Command{Shard: shardID, Node: node, Actor: actor}, record, height); err != nil {
		return nil, err
	}

	// 2. Load the peer list, or fail
	config, found, err := uc.store.Config(ctx, shardID, node)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, mesh.ErrMissingConfig
	}

	// 3. Return peers this node can't see
	unreachable := make([]vo.NodeRef, 0)
	for _, peer := range config.PeersFor(node) {
		reached, err := uc.network.Reach(ctx, shardID, node, peer)
		if err != nil {
			return nil, err
		}
		if !reached {
			unreachable = append(unreachable, peer.Node)
		}
	}
	return unreachable, nil
}
