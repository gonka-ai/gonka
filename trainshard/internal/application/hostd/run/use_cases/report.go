package usecases

import (
	"context"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type ReportUseCase struct {
	chain   shard.ChainReader
	runs    run.RunStore
	machine run.Machine
}

func NewReportUseCase(chain shard.ChainReader, runs run.RunStore, machine run.Machine) *ReportUseCase {
	return &ReportUseCase{chain: chain, runs: runs, machine: machine}
}

func (uc *ReportUseCase) Execute(ctx context.Context, cmd NodesCommand) ([]run.NodeReport, error) {
	// 1. Authorize the read
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}

	// 2. Take from the record only what a run needs from it
	reservation := run.Reservation{Shard: record.ID, BaseImage: record.BaseImage, Active: record.IsActive(height)}

	// 3. Return image history and exit codes
	return run.PerNode(cmd.Nodes, run.FailedReport, func(node vo.NodeRef) (run.NodeReport, error) {
		if err := shard.CanObserve(cmd.forNode(node), record, height); err != nil {
			return run.NodeReport{}, err
		}
		state, _, err := uc.runs.Load(ctx, node)
		if err != nil {
			return run.NodeReport{}, err
		}
		observed, err := uc.machine.Observe(ctx, node, run.DesiredFor(reservation, state, false))
		if err != nil {
			return run.NodeReport{}, err
		}
		return run.ReportOf(node, state, observed), nil
	}), nil
}
