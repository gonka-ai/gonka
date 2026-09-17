package usecases

import (
	"context"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type AbortUseCase struct {
	chain     shard.ChainReader
	submitter shard.ChainSubmitter
}

func NewAbortUseCase(chain shard.ChainReader, submitter shard.ChainSubmitter) *AbortUseCase {
	return &AbortUseCase{chain: chain, submitter: submitter}
}

func (uc *AbortUseCase) Execute(ctx context.Context, node vo.NodeRef) error {
	// 1. Load reservation, or fail
	shardID, reserved, err := uc.chain.Reservation(ctx, node)
	if err != nil {
		return err
	}
	if !reserved {
		return shard.ErrNodeNotReserved
	}

	// 2. Release the reservation
	return uc.submitter.Release(ctx, shardID, node, vo.ReleaseOperatorAbort)
}
