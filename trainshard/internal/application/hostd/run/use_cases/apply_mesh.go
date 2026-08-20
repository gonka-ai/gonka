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
	chain     shard.ChainReader
	log       run.RequestLog
	store     mesh.Store
	control   run.NodeControl
	reconcile Reconciler
	clock     ports.Clock
}

func NewApplyMeshUseCase(
	chain shard.ChainReader,
	log run.RequestLog,
	store mesh.Store,
	control run.NodeControl,
	reconcile Reconciler,
	clock ports.Clock,
) *ApplyMeshUseCase {
	return &ApplyMeshUseCase{chain: chain, log: log, store: store, control: control, reconcile: reconcile, clock: clock}
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

	// 2. Replay stored answer if seen
	recorded, found, err := uc.log.Result(ctx, cmd.request(run.OpMesh))
	if err != nil || found {
		return recorded, err
	}

	// 3. Reject peers not reserved here
	for _, peer := range cmd.Config.Refs() {
		if !record.Reserves(peer) {
			return nil, shard.ErrNodeNotReserved
		}
	}

	// 4. Store each node's peer list and bring its interface up
	results := run.PerNode(cmd.Nodes, run.Failed, func(node vo.NodeRef) (run.NodeResult, error) {
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
		if err := uc.store.SaveConfig(ctx, cmd.Shard, node, cmd.Config); err != nil {
			return run.NodeResult{}, err
		}
		if err := uc.reconcile.Execute(ctx, node); err != nil {
			return run.NodeResult{}, err
		}
		return run.NodeResult{Node: node, State: vo.ContainerUnknown}, nil
	})

	// 5. Store the answer by request id
	if err := uc.log.Record(ctx, cmd.request(run.OpMesh), results); err != nil {
		return nil, err
	}
	return results, nil
}
