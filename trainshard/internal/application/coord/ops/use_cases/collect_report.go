package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type CollectReportUseCase struct {
	chain   shard.ChainReader
	reports run.HostReports
}

func NewCollectReportUseCase(chain shard.ChainReader, reports run.HostReports) *CollectReportUseCase {
	return &CollectReportUseCase{chain: chain, reports: reports}
}

func (uc *CollectReportUseCase) Execute(ctx context.Context, cmd RunCommand) ([]run.NodeReport, error) {
	// 1. Load nodes from chain
	record, _, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}

	// 2. One call per host; return collected results
	return run.PerHost(ctx, record.Refs(), run.FailedReport, func(ctx context.Context, participant vo.Participant, nodes []vo.NodeRef) ([]run.NodeReport, error) {
		return uc.reports.Report(ctx, participant, cmd.Shard, nodes)
	}), nil
}
