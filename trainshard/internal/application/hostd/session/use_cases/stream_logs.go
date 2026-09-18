package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
)

type StreamLogsUseCase struct {
	chain   shard.ChainReader
	streams run.Streams
}

func NewStreamLogsUseCase(chain shard.ChainReader, streams run.Streams) *StreamLogsUseCase {
	return &StreamLogsUseCase{chain: chain, streams: streams}
}

func (uc *StreamLogsUseCase) Execute(ctx context.Context, cmd LogsCommand, out io.Writer) error {
	// 1. Authorize before writing
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return err
	}
	if err := shard.CanObserve(cmd.command(), record, height); err != nil {
		return err
	}

	// 2. Stream logs to out
	return uc.streams.Logs(ctx, run.LogRequest{
		Shard: cmd.Shard,
		Node:  cmd.Node,
		Since: cmd.Since,
		Tail:  cmd.Tail,
	}, out)
}
