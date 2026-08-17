package runtimeparams

import (
	"context"

	"common/chain"
	commrc "common/runtimeconfig"
	"devshard/runtimeconfig"
)

// GRPCChainFetcher adapts common/chain.Client to runtimeconfig.ChainParamsFetcher.
type GRPCChainFetcher struct {
	inner *commrc.ChainFetcher
}

// NewGRPCChainFetcher returns a fetcher that issues Params + EpochInfo over gRPC
// via common/chain (single transport shared with bridge queries).
func NewGRPCChainFetcher(client *chain.Client) *GRPCChainFetcher {
	return &GRPCChainFetcher{inner: commrc.NewChainFetcher(client)}
}

func (f *GRPCChainFetcher) FetchSnapshot(ctx context.Context) (runtimeconfig.Snapshot, error) {
	return f.inner.FetchSnapshot(ctx)
}

var _ runtimeconfig.ChainParamsFetcher = (*GRPCChainFetcher)(nil)
