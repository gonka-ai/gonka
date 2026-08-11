package fetcher

import (
	"context"
	"fmt"

	"common/chain"
	commrc "common/runtimeconfig"
	"devshard/testenv/mockchain/adminface"
)

// SnapshotFetcher loads lane-C params via inference gRPC and params_block_height
// from mock-chain admin revision (dedicated field, not PocStartBlockHeight).
type SnapshotFetcher struct {
	chain *commrc.ChainFetcher
	admin *adminface.Client
}

// New returns a fetcher for mock-dapi runtime config sync.
func New(chainClient *chain.Client, admin *adminface.Client) SnapshotFetcher {
	return SnapshotFetcher{
		chain: commrc.NewChainFetcher(chainClient),
		admin: admin,
	}
}

// FetchSnapshot implements params.SnapshotFetcher.
func (f SnapshotFetcher) FetchSnapshot(ctx context.Context) (commrc.Snapshot, error) {
	if f.chain == nil {
		return commrc.Snapshot{}, fmt.Errorf("mockchain fetcher: nil chain client")
	}
	snap, err := f.chain.FetchSnapshot(ctx)
	if err != nil {
		return commrc.Snapshot{}, err
	}
	if f.admin == nil {
		return snap, nil
	}
	rev, err := f.admin.GetRevision(ctx)
	if err != nil {
		return commrc.Snapshot{}, fmt.Errorf("mockchain fetcher: revision: %w", err)
	}
	if rev.ParamsBlockHeight > 0 {
		snap.ParamsBlockHeight = rev.ParamsBlockHeight
	}
	return snap, nil
}
