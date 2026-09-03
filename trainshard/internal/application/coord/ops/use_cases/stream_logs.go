package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
)

type StreamLogsUseCase struct {
	streams run.HostStreams
}

func NewStreamLogsUseCase(streams run.HostStreams) *StreamLogsUseCase {
	return &StreamLogsUseCase{streams: streams}
}

func (uc *StreamLogsUseCase) Execute(ctx context.Context, cmd NodeCommand, out io.Writer) error {
	// 1. Call the host that holds the node
	return uc.streams.Logs(ctx, cmd.Node.Participant, run.LogRequest{
		Shard: cmd.Shard,
		Node:  cmd.Node,
		Since: cmd.Since,
		Tail:  cmd.Tail,
	}, out)
}
