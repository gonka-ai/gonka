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

func ReadActive(ctx context.Context, chain ChainReader, shardID vo.ShardID) (Shard, error) {
	record, height, err := Read(ctx, chain, shardID)
	if err != nil {
		return Shard{}, err
	}
	if !record.IsActive(height) {
		return Shard{}, ErrShardClosed
	}
	return record, nil
}
