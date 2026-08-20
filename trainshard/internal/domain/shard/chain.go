package shard

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

func Read(ctx context.Context, chain ChainReader, shardID vo.ShardID) (Shard, vo.Height, error) {
	height, err := chain.Height(ctx)
	if err != nil {
		return Shard{}, 0, err
	}
	record, found, err := chain.Shard(ctx, shardID)
	if err != nil {
		return Shard{}, 0, err
	}
	if !found {
		return Shard{}, 0, ErrShardUnknown
	}
	return record, height, nil
}
