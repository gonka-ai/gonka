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
	// 1. Read the node's reservation from chain, or refuse an unreserved node
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
