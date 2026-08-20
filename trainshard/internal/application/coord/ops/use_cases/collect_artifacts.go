package usecases

import (
	"context"
	"io"

	"trainshard/internal/domain/run"
)

type CollectArtifactsUseCase struct {
	reports run.HostReports
}

func NewCollectArtifactsUseCase(reports run.HostReports) *CollectArtifactsUseCase {
	return &CollectArtifactsUseCase{reports: reports}
}

func (uc *CollectArtifactsUseCase) Execute(ctx context.Context, cmd NodeCommand, out io.Writer) error {
	// 1. Name the node; host streams the rest
	return uc.reports.Artifacts(ctx, cmd.Node.Participant, cmd.Shard, cmd.Node, out)
}
