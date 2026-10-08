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
	// 1. A repeated request changes nothing; return the recorded result
	return uc.once.Do(ctx, cmd.request(run.OpMesh), func(ctx context.Context) ([]run.NodeResult, error) {
		// 2. Read the shard from chain and refuse anyone it does not answer to
		record, height, err := shard.Read(ctx, uc.chain, cmd.Shard)
		if err != nil {
			return nil, err
		}
		if err := shard.CanAsk(cmd.Shard, cmd.Actor, record); err != nil {
			return nil, err
		}

		// 3. Reject a peer list naming a node the shard does not reserve
		for _, peer := range cmd.Config.Refs() {
			if !record.Reserves(peer) {
				return nil, shard.ErrNodeNotReserved
			}
		}

		// 4. Store each node's peer list and bring its interface up
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
			// the rebuild is counted before the list is saved, or a list saved alone never corrects the rank
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
			// a run that fails after the mesh is up is the tenant's to replace, not a refused peer list
			if err := uc.converge.Record(ctx, node, write); err != nil && !run.FailedOnTheRun(err) {
				return run.NodeResult{}, err
			}
			return run.NodeResult{Node: node, State: vo.ContainerUnknown}, nil
		}), nil
	})
}
