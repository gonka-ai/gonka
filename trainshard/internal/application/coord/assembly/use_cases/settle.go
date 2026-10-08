package usecases

import (
	"context"

	"trainshard/internal/domain/shard"
)

type SettleUseCase struct {
	lifecycle shard.ChainLifecycle
}

func NewSettleUseCase(lifecycle shard.ChainLifecycle) *SettleUseCase {
	return &SettleUseCase{lifecycle: lifecycle}
}

func (uc *SettleUseCase) Execute(ctx context.Context, cmd SettleCommand) error {
	// 1. Ask the chain to settle the shard.
	return uc.lifecycle.Settle(ctx, cmd.Shard)
}
