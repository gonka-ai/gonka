package devshard

import (
	"context"
	"errors"
	"testing"

	chaintypes "github.com/productscience/inference/x/inference/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeQueryClient embeds chaintypes.QueryClient (nil interface) so calls to
// any method we do not override would panic at runtime — but tests only
// touch Params and EpochInfo, which we implement explicitly.
type fakeQueryClient struct {
	chaintypes.QueryClient

	paramsFn    func(ctx context.Context, in *chaintypes.QueryParamsRequest, opts ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error)
	epochInfoFn func(ctx context.Context, in *chaintypes.QueryEpochInfoRequest, opts ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error)
}

func (f *fakeQueryClient) Params(ctx context.Context, in *chaintypes.QueryParamsRequest, opts ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
	return f.paramsFn(ctx, in, opts...)
}

func (f *fakeQueryClient) EpochInfo(ctx context.Context, in *chaintypes.QueryEpochInfoRequest, opts ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error) {
	return f.epochInfoFn(ctx, in, opts...)
}

type fakeQueryProvider struct {
	qc chaintypes.QueryClient
}

func (p fakeQueryProvider) NewInferenceQueryClient() chaintypes.QueryClient { return p.qc }

func TestChainParamsFetcher_v0_2_13Chain_MapsAllFields(t *testing.T) {
	qc := &fakeQueryClient{
		paramsFn: func(_ context.Context, _ *chaintypes.QueryParamsRequest, _ ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
			return &chaintypes.QueryParamsResponse{
				Params: chaintypes.Params{
					ValidationParams: &chaintypes.ValidationParams{LogprobsMode: "raw"},
					DevshardEscrowParams: &chaintypes.DevshardEscrowParams{
						DevshardRequestsEnabled:           true,
						MaxNonce:                          1500,
						RefusalTimeout:                    60,
						ExecutionTimeout:                  1200,
						ValidationRate:                    5000,
						VoteThresholdFactor: 50,
					},
				},
			}, nil
		},
		epochInfoFn: func(_ context.Context, _ *chaintypes.QueryEpochInfoRequest, _ ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error) {
			return &chaintypes.QueryEpochInfoResponse{
				LatestEpoch: chaintypes.Epoch{Index: 7, PocStartBlockHeight: 4242},
			}, nil
		},
	}

	f := NewChainParamsFetcher(fakeQueryProvider{qc: qc})
	snap, err := f.FetchSnapshot(context.Background())
	require.NoError(t, err)

	assert.Equal(t, int64(4242), snap.ParamsBlockHeight)
	assert.Equal(t, uint64(7), snap.CurrentEpochID)
	assert.Equal(t, "raw", snap.LogprobsMode)
	assert.True(t, snap.DevshardRequestsEnabled)
	assert.Equal(t, uint32(1500), snap.MaxNonce)
	assert.Equal(t, int64(60), snap.RefusalTimeout)
	assert.Equal(t, int64(1200), snap.ExecutionTimeout)
	assert.Equal(t, uint32(5000), snap.ValidationRate)
	assert.Equal(t, uint32(50), snap.VoteThresholdFactor)
	assert.Nil(t, snap.ApprovedVersions, "approved_versions are not on chain; stay nil")
	assert.True(t, snap.ServedAt.IsZero(), "ServedAt is stamped by chainProvider, not the fetcher")
}

func TestChainParamsFetcher_v0_2_12Chain_NewFieldsZero(t *testing.T) {
	qc := &fakeQueryClient{
		paramsFn: func(_ context.Context, _ *chaintypes.QueryParamsRequest, _ ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
			// v0.2.12 chain: DevshardEscrowParams missing the new v0.2.13
			// fields. Proto3 default decode leaves them at zero.
			return &chaintypes.QueryParamsResponse{
				Params: chaintypes.Params{
					ValidationParams: &chaintypes.ValidationParams{LogprobsMode: "processed"},
					DevshardEscrowParams: &chaintypes.DevshardEscrowParams{
						DevshardRequestsEnabled: true,
						MaxNonce:                500,
					},
				},
			}, nil
		},
		epochInfoFn: func(_ context.Context, _ *chaintypes.QueryEpochInfoRequest, _ ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error) {
			return &chaintypes.QueryEpochInfoResponse{
				LatestEpoch: chaintypes.Epoch{Index: 1, PocStartBlockHeight: 100},
			}, nil
		},
	}

	f := NewChainParamsFetcher(fakeQueryProvider{qc: qc})
	snap, err := f.FetchSnapshot(context.Background())
	require.NoError(t, err)

	assert.True(t, snap.DevshardRequestsEnabled)
	assert.Equal(t, uint32(500), snap.MaxNonce)
	// v0.2.13 fields must be zero so ApplyLiveSessionParams compiled defaults
	// kick in (state-root determinism).
	assert.Equal(t, int64(0), snap.RefusalTimeout)
	assert.Equal(t, int64(0), snap.ExecutionTimeout)
	assert.Equal(t, uint32(0), snap.ValidationRate)
	assert.Equal(t, uint32(0), snap.VoteThresholdFactor)
}

func TestChainParamsFetcher_DevshardEscrowParamsNil_KeepsZeros(t *testing.T) {
	qc := &fakeQueryClient{
		paramsFn: func(_ context.Context, _ *chaintypes.QueryParamsRequest, _ ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
			return &chaintypes.QueryParamsResponse{
				Params: chaintypes.Params{
					ValidationParams:     &chaintypes.ValidationParams{LogprobsMode: "processed"},
					DevshardEscrowParams: nil,
				},
			}, nil
		},
		epochInfoFn: func(_ context.Context, _ *chaintypes.QueryEpochInfoRequest, _ ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error) {
			return &chaintypes.QueryEpochInfoResponse{
				LatestEpoch: chaintypes.Epoch{Index: 0, PocStartBlockHeight: 0},
			}, nil
		},
	}

	f := NewChainParamsFetcher(fakeQueryProvider{qc: qc})
	snap, err := f.FetchSnapshot(context.Background())
	require.NoError(t, err)
	assert.False(t, snap.DevshardRequestsEnabled, "no devshard params on chain ⇒ disabled")
	assert.Equal(t, uint32(0), snap.MaxNonce)
}

func TestChainParamsFetcher_ParamsError_Wrapped(t *testing.T) {
	wantErr := errors.New("chain RPC down")
	qc := &fakeQueryClient{
		paramsFn: func(context.Context, *chaintypes.QueryParamsRequest, ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
			return nil, wantErr
		},
	}
	f := NewChainParamsFetcher(fakeQueryProvider{qc: qc})
	_, err := f.FetchSnapshot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "query params")
}

func TestChainParamsFetcher_EpochInfoError_Wrapped(t *testing.T) {
	wantErr := errors.New("epoch lookup failed")
	qc := &fakeQueryClient{
		paramsFn: func(context.Context, *chaintypes.QueryParamsRequest, ...grpc.CallOption) (*chaintypes.QueryParamsResponse, error) {
			return &chaintypes.QueryParamsResponse{
				Params: chaintypes.Params{
					ValidationParams: &chaintypes.ValidationParams{LogprobsMode: "raw"},
				},
			}, nil
		},
		epochInfoFn: func(context.Context, *chaintypes.QueryEpochInfoRequest, ...grpc.CallOption) (*chaintypes.QueryEpochInfoResponse, error) {
			return nil, wantErr
		},
	}
	f := NewChainParamsFetcher(fakeQueryProvider{qc: qc})
	_, err := f.FetchSnapshot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "query epoch info")
}
