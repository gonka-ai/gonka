package usecases

import (
	"context"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type AssembleUseCase struct {
	lifecycle shard.ChainLifecycle
}

func NewAssembleUseCase(lifecycle shard.ChainLifecycle) *AssembleUseCase {
	return &AssembleUseCase{lifecycle: lifecycle}
}

func (uc *AssembleUseCase) Execute(ctx context.Context, cmd AssembleCommand) (vo.ShardID, error) {
	// 1. Ask the chain to assemble the proposal into a shard.
	return uc.lifecycle.Assemble(ctx, cmd.Proposal)
}
