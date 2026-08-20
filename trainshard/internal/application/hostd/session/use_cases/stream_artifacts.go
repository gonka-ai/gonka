package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
)

type StreamArtifactsUseCase struct {
	chain   shard.ChainReader
	volumes run.Volumes
}

func NewStreamArtifactsUseCase(chain shard.ChainReader, volumes run.Volumes) *StreamArtifactsUseCase {
	return &StreamArtifactsUseCase{chain: chain, volumes: volumes}
}

func (uc *StreamArtifactsUseCase) Execute(ctx context.Context, cmd SessionCommand, out io.Writer) error {
	// 1. Authorize before writing
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return err
	}
	if err := shard.CanObserve(cmd.command(), record, height); err != nil {
		return err
	}

	// 2. Copy leftover volume to out
	return uc.volumes.Archive(ctx, cmd.Shard, cmd.Node, out)
}
