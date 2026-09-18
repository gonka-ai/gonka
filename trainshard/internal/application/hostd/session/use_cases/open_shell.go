package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
)

type OpenShellUseCase struct {
	chain    shard.ChainReader
	streams  run.Streams
	sessions run.SessionLog
	clock    ports.Clock
}

func NewOpenShellUseCase(chain shard.ChainReader, streams run.Streams, sessions run.SessionLog, clock ports.Clock) *OpenShellUseCase {
	return &OpenShellUseCase{chain: chain, streams: streams, sessions: sessions, clock: clock}
}

func (uc *OpenShellUseCase) Execute(ctx context.Context, cmd SessionCommand, session io.ReadWriter) error {
	// 1. Authorize
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return err
	}
	if err := shard.CanObserve(cmd.command(), record, height); err != nil {
		return err
	}

	// 2. Open the session record
	sink, err := uc.sessions.Record(ctx, cmd.Shard, cmd.Node, uc.clock.Now())
	if err != nil {
		return err
	}
	defer sink.Close()

	// 3. Copy both ways into it
	return uc.streams.Shell(ctx, run.ExecRequest{Shard: cmd.Shard, Node: cmd.Node}, run.Recorded(session, sink))
}
