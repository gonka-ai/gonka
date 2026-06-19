package devshard

import (
	"context"
	"fmt"

	"devshard/runtimeconfig"

	chaintypes "github.com/productscience/inference/x/inference/types"
)

// ChainParamsFetcher adapts a cosmos inferencetypes.QueryClient into the
// runtimeconfig.ChainParamsFetcher surface used by the chain-poll provider.
// It performs one Params + EpochInfo query pair per call and builds a
// fully-populated Snapshot.
//
// Lives here (decentralized-api/internal/devshard) instead of the devshard
// module so devshard/runtimeconfig stays free of inference-chain imports;
// devshard/go.mod intentionally does not depend on inference-chain.
type ChainParamsFetcher struct {
	qcp InferenceQueryClientProvider
}

// NewChainParamsFetcher returns a fetcher that issues queries through qcp.
func NewChainParamsFetcher(qcp InferenceQueryClientProvider) *ChainParamsFetcher {
	return &ChainParamsFetcher{qcp: qcp}
}

// FetchSnapshot performs Params + EpochInfo and returns a Snapshot suitable
// for runtimeconfig.chainProvider.apply. Errors from either call are wrapped
// so the caller can distinguish them in logs.
//
// Snapshot fields populated from chain:
//   - Params.ValidationParams.LogprobsMode (empty → defaultLogprobsMode in chainProvider.refresh)
//   - Params.DevshardEscrowParams.DevshardRequestsEnabled, MaxNonce
//   - Params.DevshardEscrowParams v0.2.13 fields (zero on v0.2.12 chain ⇒
//     compiled defaults via ApplyLiveSessionParams "if > 0 override" semantics)
//   - EpochInfo.LatestEpoch.Index → CurrentEpochID
//   - EpochInfo.LatestEpoch.PocStartBlockHeight → ParamsBlockHeight
//     (used as a non-zero monotone-per-epoch proxy so the OnEpochChange gate
//     `prev.ParamsBlockHeight > 0` opens after the first refresh)
//
// Not populated:
//   - ApprovedVersions (lives in dapi versions cache, not on chain). Stays nil.
//   - ServedAt (stamped by chainProvider.refresh at apply time).
func (f *ChainParamsFetcher) FetchSnapshot(ctx context.Context) (runtimeconfig.Snapshot, error) {
	qc := f.qcp.NewInferenceQueryClient()

	paramsResp, err := qc.Params(ctx, &chaintypes.QueryParamsRequest{})
	if err != nil {
		return runtimeconfig.Snapshot{}, fmt.Errorf("query params: %w", err)
	}
	epochResp, err := qc.EpochInfo(ctx, &chaintypes.QueryEpochInfoRequest{})
	if err != nil {
		return runtimeconfig.Snapshot{}, fmt.Errorf("query epoch info: %w", err)
	}

	out := runtimeconfig.Snapshot{
		ParamsBlockHeight: epochResp.LatestEpoch.PocStartBlockHeight,
		CurrentEpochID:    epochResp.LatestEpoch.Index,
		LogprobsMode:      paramsResp.Params.ValidationParams.GetLogprobsMode(),
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

// Compile-time guard.
var _ runtimeconfig.ChainParamsFetcher = (*ChainParamsFetcher)(nil)
