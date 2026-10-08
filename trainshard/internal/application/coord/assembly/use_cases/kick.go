package usecases

import (
	"context"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type KickUseCase struct {
	submitter shard.ChainSubmitter
}

func NewKickUseCase(submitter shard.ChainSubmitter) *KickUseCase {
	return &KickUseCase{submitter: submitter}
}

func (uc *KickUseCase) Execute(ctx context.Context, cmd KickCommand) error {
	// 1. Release the node from the shard as a manual kick.
	return uc.submitter.Release(ctx, cmd.Shard, cmd.Node, vo.ReleaseManualKick)
}
