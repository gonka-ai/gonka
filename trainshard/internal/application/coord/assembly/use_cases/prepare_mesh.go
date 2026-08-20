package usecases

import (
	"context"
	"time"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
	"trainshard/internal/utils/timex"
)

type Released struct {
	Node   vo.NodeRef
	Reason vo.ReleaseReason
}

type PrepareResult struct {
	Config   mesh.Config
	Released []Released
	Failed   []mesh.Pair
}

type PrepareMeshUseCase struct {
	chain     shard.ChainReader
	hosts     mesh.Hosts
	verifier  ports.Verifier
	submitter shard.ChainSubmitter
	clock     ports.Clock
	poll      time.Duration
	settle    time.Duration
}

func NewPrepareMeshUseCase(chain shard.ChainReader, hosts mesh.Hosts, verifier ports.Verifier, submitter shard.ChainSubmitter, clock ports.Clock, poll, settle time.Duration) *PrepareMeshUseCase {
	return &PrepareMeshUseCase{chain: chain, hosts: hosts, verifier: verifier, submitter: submitter, clock: clock, poll: poll, settle: settle}
}

func (uc *PrepareMeshUseCase) Execute(ctx context.Context, shardID vo.ShardID, deadline time.Time) (PrepareResult, error) {
	released := make([]Released, 0)
	var kicked time.Time

	for {
		// 1. Load nodes from chain
		record, height, err := shard.Read(ctx, uc.chain, shardID)
		if err != nil {
			return PrepareResult{}, err
		}
		if !record.IsActive(height) {
			return PrepareResult{}, shard.ErrShardClosed
		}

		// 2. Wait until releases land, a chain needs a block or two to catch up
		if record.ReservesAny(refs(released)) {
			if !uc.clock.Now().Before(kicked.Add(uc.settle)) {
				return PrepareResult{}, shard.ErrReleasePending
			}
			if err := timex.Sleep(ctx, uc.poll); err != nil {
				return PrepareResult{}, err
			}
			continue
		}

		// 3. Collect signed members
		members, missing, err := mesh.Collect(ctx, uc.hosts, uc.verifier, shardID, record.Participants(), record.Refs())
		if err != nil {
			return PrepareResult{}, err
		}

		// 4. Give a quiet node until the deadline, then drop it and go on without it
		if len(missing) > 0 {
			if uc.clock.Now().Before(deadline) {
				if err := timex.Sleep(ctx, uc.poll); err != nil {
					return PrepareResult{}, err
				}
				continue
			}
			for _, node := range missing {
				if err := uc.submitter.Release(ctx, shardID, node, vo.ReleaseFailedPrepare); err != nil {
					return PrepareResult{}, err
				}
				released = append(released, Released{Node: node, Reason: vo.ReleaseFailedPrepare})
			}
			kicked = uc.clock.Now()
			continue
		}

		// 5. Rank them
		config, err := mesh.Order(shardID, members)
		if err != nil {
			return PrepareResult{}, err
		}

		// 6. Hand out peer lists, every host at once
		handed := syncx.Fan(config.Refs(), func(node vo.NodeRef) error {
			return uc.hosts.Apply(ctx, config, node)
		})
		for _, err := range handed {
			if err != nil {
				return PrepareResult{}, err
			}
		}

		// 7. Return if fully connected
		failed := mesh.Probe(ctx, uc.hosts, config)
		if mesh.FullyConnected(config.Refs(), failed) {
			return PrepareResult{Config: config, Released: released}, nil
		}

		// 8. Kick the worst node and retry
		worst, found := mesh.Worst(config.Refs(), failed)
		if !found {
			return PrepareResult{Released: released, Failed: failed}, nil
		}
		if err := uc.submitter.Release(ctx, shardID, worst, vo.ReleaseUnreachable); err != nil {
			return PrepareResult{}, err
		}
		released = append(released, Released{Node: worst, Reason: vo.ReleaseUnreachable})
		kicked = uc.clock.Now()
	}
}

func refs(released []Released) []vo.NodeRef {
	nodes := make([]vo.NodeRef, 0, len(released))
	for _, entry := range released {
		nodes = append(nodes, entry.Node)
	}
	return nodes
}
