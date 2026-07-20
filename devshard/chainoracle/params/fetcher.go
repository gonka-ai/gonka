package params

import (
	"context"

	"common/chain"
	commrc "common/runtimeconfig"
)

// SnapshotFetcher loads params + epoch from chain.
type SnapshotFetcher interface {
	FetchSnapshot(ctx context.Context) (commrc.Snapshot, error)
}

// StaticFetcher returns a fixed snapshot on every fetch.
type StaticFetcher struct {
	Snap commrc.Snapshot
}

func (f StaticFetcher) FetchSnapshot(context.Context) (commrc.Snapshot, error) {
	return f.Snap, nil
}

// ChainClientFetcher reads params via common/chain (Phase 2b transport).
type ChainClientFetcher struct {
	inner *commrc.ChainFetcher
}

// NewChainClientFetcher wraps common/chain.Client as a SnapshotFetcher.
func NewChainClientFetcher(client *chain.Client) SnapshotFetcher {
	return ChainClientFetcher{inner: commrc.NewChainFetcher(client)}
}

func (f ChainClientFetcher) FetchSnapshot(ctx context.Context) (commrc.Snapshot, error) {
	return f.inner.FetchSnapshot(ctx)
}
