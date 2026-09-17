package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
)

type OpenShellUseCase struct {
	streams run.HostStreams
}

func NewOpenShellUseCase(streams run.HostStreams) *OpenShellUseCase {
	return &OpenShellUseCase{streams: streams}
}

func (uc *OpenShellUseCase) Execute(ctx context.Context, cmd NodeCommand, session io.ReadWriter) error {
	// 1. Call the host that holds the node
	return uc.streams.Shell(ctx, cmd.Node.Participant, run.ExecRequest{Shard: cmd.Shard, Node: cmd.Node}, session)
}
