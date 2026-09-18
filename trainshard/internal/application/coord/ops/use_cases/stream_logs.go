package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
)

type StreamLogsUseCase struct {
	chain   shard.ChainReader
	streams run.HostStreams
}

func NewStreamLogsUseCase(chain shard.ChainReader, streams run.HostStreams) *StreamLogsUseCase {
	return &StreamLogsUseCase{chain: chain, streams: streams}
}

func (uc *StreamLogsUseCase) Execute(ctx context.Context, cmd NodeCommand, out io.Writer) error {
	// 1. Find the machine the chain says holds the node
	record, _, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return err
	}
	host, found := record.HostOf(cmd.Node)
	if !found {
		return shard.ErrNodeNotReserved
	}

	// 2. Copy the node's output from it
	return uc.streams.Logs(ctx, host, run.LogRequest{
		Shard: cmd.Shard,
		Node:  cmd.Node,
		Since: cmd.Since,
		Tail:  cmd.Tail,
	}, out)
}
