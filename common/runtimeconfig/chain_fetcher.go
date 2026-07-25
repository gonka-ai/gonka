package runtimeconfig

import (
	"context"
	"fmt"

	"common/chain"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// ChainFetcher reads Params + EpochInfo via common/chain and returns Snapshot.
type ChainFetcher struct {
	client chain.InferenceClient
}

// NewChainFetcher builds a fetcher on top of common/chain.Client.
func NewChainFetcher(c *chain.Client) *ChainFetcher {
	if c == nil {
		return nil
	}
	return &ChainFetcher{client: c.InferenceQueryClient()}
}

// NewChainFetcherFromClient builds a fetcher from any InferenceClient (tests).
func NewChainFetcherFromClient(client chain.InferenceClient) *ChainFetcher {
	return &ChainFetcher{client: client}
}

// FetchSnapshot performs one Params + EpochInfo query pair.
func (f *ChainFetcher) FetchSnapshot(ctx context.Context) (Snapshot, error) {
	if f == nil || f.client == nil {
		return Snapshot{}, fmt.Errorf("runtimeconfig: nil chain fetcher")
	}

	paramsResp, err := f.client.Params(ctx, &inferencetypes.QueryParamsRequest{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("query params: %w", err)
	}
	epochResp, err := f.client.EpochInfo(ctx, &inferencetypes.QueryEpochInfoRequest{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("query epoch info: %w", err)
	}

	out := Snapshot{
		ParamsBlockHeight: epochResp.LatestEpoch.PocStartBlockHeight,
		CurrentEpochID:    epochResp.LatestEpoch.Index,
	}
	if vp := paramsResp.Params.ValidationParams; vp != nil {
		out.LogprobsMode = vp.GetLogprobsMode()
	}
	if dep := paramsResp.Params.DevshardEscrowParams; dep != nil {
			out.DevshardRequestsEnabled = dep.DevshardRequestsEnabled
			out.MaxNonce = dep.MaxNonce
			out.RefusalTimeout = dep.RefusalTimeout
			out.ExecutionTimeout = dep.ExecutionTimeout
			out.ValidationRate = dep.ValidationRate
			out.VoteThresholdFactor = dep.VoteThresholdFactor
	}
	return out, nil
}
