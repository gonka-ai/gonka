package usecases

import (
	"context"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type ApplyMeshUseCase struct {
	chain    shard.ChainReader
	once     *run.Once
	store    mesh.Store
	runs     run.RunStore
	control  run.NodeControl
	converge *run.Converger
	clock    ports.Clock
}

func NewApplyMeshUseCase(
	chain shard.ChainReader,
	once *run.Once,
	store mesh.Store,
	runs run.RunStore,
	control run.NodeControl,
	converge *run.Converger,
	clock ports.Clock,
) *ApplyMeshUseCase {
	return &ApplyMeshUseCase{chain: chain, once: once, store: store, runs: runs, control: control, converge: converge, clock: clock}
}

func (uc *ApplyMeshUseCase) Execute(ctx context.Context, cmd MeshCommand) ([]run.NodeResult, error) {
	// 1. Read the shard from chain
	record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
	if err != nil {
		return nil, err
	}
	if err := shard.CanAsk(cmd.Shard, cmd.Actor, record); err != nil {
		return nil, err
	}

	// 2. Reject peers not reserved here
	for _, peer := range cmd.Config.Refs() {
		if !record.Reserves(peer) {
			return nil, shard.ErrNodeNotReserved
		}
	}

	// 3. Answer once per request: store each node's peer list and bring its interface up
	return uc.once.Do(ctx, cmd.request(run.OpMesh), func(ctx context.Context) []run.NodeResult {
		return run.PerNode(cmd.Nodes, run.Failed, func(node vo.NodeRef) (run.NodeResult, error) {
			if !cmd.Config.Contains(node) {
				return run.NodeResult{}, mesh.ErrNodeNotInMesh
			}
			drained, err := uc.control.Drained(ctx, node)
			if err != nil {
				return run.NodeResult{}, err
			}
			if err := shard.CanApplyMesh(cmd.forNode(node), record, drained, uc.clock.Now(), height); err != nil {
				return run.NodeResult{}, err
			}
			// revision first: a counted rebuild that fails to land rebuilds the same place; a list
			// saved without one never corrects the rank
			write := func(ctx context.Context) error {
				previous, had, err := uc.store.Config(ctx, cmd.Shard, node)
				if err != nil {
					return err
				}
				if had && mesh.Rebuilds(previous, cmd.Config, node) {
					if err := run.RecordRebuild(ctx, uc.runs, node); err != nil {
						return err
					}
				}
				return uc.store.SaveConfig(ctx, cmd.Shard, node, cmd.Config)
			}
			// the list is taken once the pass gets past the mesh: a run that falls over after it is
			// the tenant's to replace, and refusing the list for it has the node kicked at the deadline
			if err := uc.converge.Record(ctx, node, write); err != nil && !run.FailedOnTheRun(err) {
				return run.NodeResult{}, err
			}
			return run.NodeResult{Node: node, State: vo.ContainerUnknown}, nil
		})
	})
}
