package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
)

type OpenShellUseCase struct {
	chain   shard.ChainReader
	streams run.HostStreams
}

func NewOpenShellUseCase(chain shard.ChainReader, streams run.HostStreams) *OpenShellUseCase {
	return &OpenShellUseCase{chain: chain, streams: streams}
}

func (uc *OpenShellUseCase) Execute(ctx context.Context, cmd NodeCommand, session io.ReadWriter) error {
	// 1. Find the machine the chain says holds the node
	record, _, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return err
	}
	host, found := record.HostOf(cmd.Node)
	if !found {
		return shard.ErrNodeNotReserved
	}

	// 2. Open the session in the node's container there
	return uc.streams.Shell(ctx, host, run.ExecRequest{Shard: cmd.Shard, Node: cmd.Node}, session)
}
