package internal

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeEpochQueryClient implements only the two query methods the cache uses; the
// embedded (nil) interface satisfies the rest and panics if anything else is called.
type fakeEpochQueryClient struct {
	types.QueryClient
	currentFn func(ctx context.Context) (*types.QueryCurrentEpochGroupDataResponse, error)
	epochFn   func(ctx context.Context, in *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error)
}

func (f *fakeEpochQueryClient) CurrentEpochGroupData(ctx context.Context, in *types.QueryCurrentEpochGroupDataRequest, opts ...grpc.CallOption) (*types.QueryCurrentEpochGroupDataResponse, error) {
	return f.currentFn(ctx)
}

func (f *fakeEpochQueryClient) EpochGroupData(ctx context.Context, in *types.QueryGetEpochGroupDataRequest, opts ...grpc.CallOption) (*types.QueryGetEpochGroupDataResponse, error) {
	return f.epochFn(ctx, in)
}

type fakeEpochRecorder struct {
	cosmosclient.CosmosMessageClient
	qc types.QueryClient
}

func (r *fakeEpochRecorder) NewInferenceQueryClient() types.QueryClient { return r.qc }

// TestGetCurrentEpochGroupDataSingleflightsAndReleasesLock proves the epoch-boundary
// fix: concurrent misses coalesce into a single chain query (singleflight), the
// query runs with a bounded context, and the cache mutex is NOT held across it, so
// one slow/hung query cannot stall every inference-validation caller.
func TestGetCurrentEpochGroupDataSingleflightsAndReleasesLock(t *testing.T) {
	var calls int32
	var deadlineSeen atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	qc := &fakeEpochQueryClient{currentFn: func(ctx context.Context) (*types.QueryCurrentEpochGroupDataResponse, error) {
		atomic.AddInt32(&calls, 1)
		if _, ok := ctx.Deadline(); ok {
			deadlineSeen.Store(true)
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return &types.QueryCurrentEpochGroupDataResponse{}, nil
	}}
	cache := NewEpochGroupDataCache(&fakeEpochRecorder{qc: qc})

	const N = 8
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cache.GetCurrentEpochGroupData(7)
			errs <- err
		}()
	}

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no RPC started")
	}

	// The mutex must not be held across the (still-blocked) RPC.
	locked := make(chan struct{})
	go func() {
		cache.mu.RLock()
		cache.mu.RUnlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("cache mutex held across the RPC - a hung query would stall all callers")
	}

	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "singleflight must coalesce concurrent misses into one RPC")
	require.True(t, deadlineSeen.Load(), "the chain query must run with a bounded (deadline) context")

	// A subsequent call for the same epoch is served from cache (no new RPC).
	_, err := cache.GetCurrentEpochGroupData(7)
	require.NoError(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "cache hit must not issue another RPC")
}

// TestGetEpochGroupDataCaches confirms the multi-epoch path also queries once and
// then serves from cache.
func TestGetEpochGroupDataCaches(t *testing.T) {
	var calls int32
	qc := &fakeEpochQueryClient{epochFn: func(ctx context.Context, in *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
		atomic.AddInt32(&calls, 1)
		return &types.QueryGetEpochGroupDataResponse{}, nil
	}}
	cache := NewEpochGroupDataCache(&fakeEpochRecorder{qc: qc})

	for i := 0; i < 3; i++ {
		_, err := cache.GetEpochGroupData(context.Background(), 42)
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "repeated gets for the same epoch must hit the cache")
}
