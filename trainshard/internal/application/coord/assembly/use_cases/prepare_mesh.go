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
	chain      shard.ChainReader
	hosts      mesh.Hosts
	verifier   ports.Verifier
	delegation ports.Delegation
	submitter  shard.ChainSubmitter
	clock      ports.Clock
	poll       time.Duration
	settle     time.Duration
}

func NewPrepareMeshUseCase(chain shard.ChainReader, hosts mesh.Hosts, verifier ports.Verifier, delegation ports.Delegation, submitter shard.ChainSubmitter, clock ports.Clock, poll, settle time.Duration) *PrepareMeshUseCase {
	return &PrepareMeshUseCase{chain: chain, hosts: hosts, verifier: verifier, delegation: delegation, submitter: submitter, clock: clock, poll: poll, settle: settle}
}

func (uc *PrepareMeshUseCase) Execute(ctx context.Context, shardID vo.ShardID, deadline time.Time) (PrepareResult, error) {
	released := make([]Released, 0)
	var kicked time.Time

	for {
		// 1. Load nodes from chain, refusing a shard that is already over
		record, err := shard.ReadActive(ctx, uc.chain, shardID)
		if err != nil {
			return PrepareResult{}, err
		}

		// 2. Wait until releases land; the chain needs a block or two to catch up
		releasedNodes := make([]vo.NodeRef, 0, len(released))
		for _, entry := range released {
			releasedNodes = append(releasedNodes, entry.Node)
		}
		if record.ReservesAny(releasedNodes) {
			if !uc.clock.Now().Before(kicked.Add(uc.settle)) {
				return PrepareResult{}, shard.ErrReleasePending
			}
			if err := timex.Sleep(ctx, uc.poll); err != nil {
				return PrepareResult{}, err
			}
			continue
		}

		// 3. Drop a node the chain holds no address for at once; a shard never gains one later
		if unaddressed := record.Unaddressed(); len(unaddressed) > 0 {
			gone := make([]Released, 0, len(unaddressed))
			for _, node := range unaddressed {
				if err := uc.submitter.Release(ctx, shardID, node, vo.ReleaseUnreachable); err != nil {
					return PrepareResult{}, err
				}
				gone = append(gone, Released{Node: node, Reason: vo.ReleaseUnreachable})
			}
			released, kicked = append(released, gone...), uc.clock.Now()
			continue
		}

		// 4. Collect signed members
		members, missing, err := mesh.Collect(ctx, uc.hosts, uc.verifier, uc.delegation, shardID, record.Hosts(), record.Refs())
		if err != nil {
			return PrepareResult{}, err
		}

		// 5. Give a quiet node until the deadline, then drop it and go on without it
		if len(missing) > 0 {
			if uc.clock.Now().Before(deadline) {
				if err := timex.Sleep(ctx, uc.poll); err != nil {
					return PrepareResult{}, err
				}
				continue
			}
			gone := make([]Released, 0, len(missing))
			for _, node := range missing {
				if err := uc.submitter.Release(ctx, shardID, node, vo.ReleaseFailedPrepare); err != nil {
					return PrepareResult{}, err
				}
				gone = append(gone, Released{Node: node, Reason: vo.ReleaseFailedPrepare})
			}
			released, kicked = append(released, gone...), uc.clock.Now()
			continue
		}

		// 6. Rank the members
		config, err := mesh.Order(shardID, members)
		if err != nil {
			return PrepareResult{}, err
		}

		// 7. Hand out peer lists, every host at once, and drop whoever will not take one
		nodes, machines := config.Refs(), record.Hosts()
		refused := make([]vo.NodeRef, 0)
		for index, err := range syncx.Fan(nodes, func(node vo.NodeRef) error {
			host, found := vo.HostOf(machines, node)
			if !found {
				return shard.ErrNodeNotReserved
			}
			return uc.hosts.Apply(ctx, config, host, node)
		}) {
			if err != nil {
				refused = append(refused, nodes[index])
			}
		}
		if len(refused) > 0 {
			// Apply reports a restarting host the same as a refusing one, so both wait for the deadline
			if uc.clock.Now().Before(deadline) {
				if err := timex.Sleep(ctx, uc.poll); err != nil {
					return PrepareResult{}, err
				}
				continue
			}
			gone := make([]Released, 0, len(refused))
			for _, node := range refused {
				if err := uc.submitter.Release(ctx, shardID, node, vo.ReleaseFailedPrepare); err != nil {
					return PrepareResult{}, err
				}
				gone = append(gone, Released{Node: node, Reason: vo.ReleaseFailedPrepare})
			}
			released, kicked = append(released, gone...), uc.clock.Now()
			continue
		}

		// 8. Return if fully connected
		failed := mesh.Probe(ctx, uc.hosts, config, record.Hosts())
		if mesh.FullyConnected(nodes, failed) {
			return PrepareResult{Config: config, Released: released}, nil
		}

		// 9. Give the tunnels until the deadline, and a mesh reshaped by a kick its settle window
		if now := uc.clock.Now(); now.Before(deadline) || now.Before(kicked.Add(uc.settle)) {
			if err := timex.Sleep(ctx, uc.poll); err != nil {
				return PrepareResult{}, err
			}
			continue
		}

		// 10. Kick the worst node and retry
		worst, found := mesh.Worst(nodes, failed)
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
