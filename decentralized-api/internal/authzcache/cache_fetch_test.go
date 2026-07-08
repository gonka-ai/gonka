package authzcache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeAuthzQueryClient implements only the two query methods getSigners uses; the
// embedded (nil) interface satisfies the rest and panics if anything else runs.
type fakeAuthzQueryClient struct {
	types.QueryClient
	granteesFn func(ctx context.Context) (*types.QueryGranteesByMessageTypeResponse, error)
	accountFn  func(ctx context.Context) (*types.QueryAccountByAddressResponse, error)
}

func (f *fakeAuthzQueryClient) GranteesByMessageType(ctx context.Context, in *types.QueryGranteesByMessageTypeRequest, opts ...grpc.CallOption) (*types.QueryGranteesByMessageTypeResponse, error) {
	return f.granteesFn(ctx)
}

func (f *fakeAuthzQueryClient) AccountByAddress(ctx context.Context, in *types.QueryAccountByAddressRequest, opts ...grpc.CallOption) (*types.QueryAccountByAddressResponse, error) {
	return f.accountFn(ctx)
}

type fakeAuthzRecorder struct {
	cosmosclient.CosmosMessageClient
	qc types.QueryClient
}

func (r *fakeAuthzRecorder) NewInferenceQueryClient() types.QueryClient { return r.qc }

// TestGetSignersSingleflightsAndReleasesLock proves that concurrent misses for
// the same granter coalesce into one pair of chain queries, and that the cache
// mutex is not held across the queries, so a slow granter cannot throttle
// verification for every other granter.
func TestGetSignersSingleflightsAndReleasesLock(t *testing.T) {
	var granteesCalls int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	qc := &fakeAuthzQueryClient{
		granteesFn: func(ctx context.Context) (*types.QueryGranteesByMessageTypeResponse, error) {
			atomic.AddInt32(&granteesCalls, 1)
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return &types.QueryGranteesByMessageTypeResponse{Grantees: []*types.Grantee{{Address: "g1", PubKey: "pk1"}}}, nil
		},
		accountFn: func(ctx context.Context) (*types.QueryAccountByAddressResponse, error) {
			return &types.QueryAccountByAddressResponse{Pubkey: "granterpk"}, nil
		},
	}
	cache := NewAuthzCache(&fakeAuthzRecorder{qc: qc})

	const N = 8
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.GetPubKeys(context.Background(), "granter", "msg")
		}()
	}

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no RPC started")
	}

	locked := make(chan struct{})
	go func() {
		cache.mu.RLock()
		cache.mu.RUnlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("cache mutex held across the RPC - one slow granter would throttle everyone")
	}

	close(release)
	wg.Wait()
	require.Equal(t, int32(1), atomic.LoadInt32(&granteesCalls), "singleflight must coalesce concurrent misses into one query pair")
}

// TestGetPubKeyForSignerAndCaching covers correctness of the fetch path and that a
// single fetch populates the cache for all subsequent lookups within the TTL.
func TestGetPubKeyForSignerAndCaching(t *testing.T) {
	var calls int32
	qc := &fakeAuthzQueryClient{
		granteesFn: func(ctx context.Context) (*types.QueryGranteesByMessageTypeResponse, error) {
			atomic.AddInt32(&calls, 1)
			return &types.QueryGranteesByMessageTypeResponse{Grantees: []*types.Grantee{{Address: "grantee1", PubKey: "gpk1"}}}, nil
		},
		accountFn: func(ctx context.Context) (*types.QueryAccountByAddressResponse, error) {
			return &types.QueryAccountByAddressResponse{Pubkey: "granterpk"}, nil
		},
	}
	cache := NewAuthzCache(&fakeAuthzRecorder{qc: qc})

	pk, err := cache.GetPubKeyForSigner(context.Background(), "granter", "grantee1", "msg")
	require.NoError(t, err)
	require.Equal(t, "gpk1", pk)

	pk, err = cache.GetPubKeyForSigner(context.Background(), "granter", "granter", "msg")
	require.NoError(t, err)
	require.Equal(t, "granterpk", pk)

	pk, err = cache.GetPubKeyForSigner(context.Background(), "granter", "nobody", "msg")
	require.NoError(t, err)
	require.Equal(t, "", pk, "unknown signer returns empty, not an error")

	pks, err := cache.GetPubKeys(context.Background(), "granter", "msg")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"gpk1", "granterpk"}, pks)

	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "all lookups served from a single cached fetch")
}

func TestGetSignersPropagatesRPCError(t *testing.T) {
	qc := &fakeAuthzQueryClient{
		granteesFn: func(ctx context.Context) (*types.QueryGranteesByMessageTypeResponse, error) {
			return nil, fmt.Errorf("chain unavailable")
		},
		accountFn: func(ctx context.Context) (*types.QueryAccountByAddressResponse, error) {
			return &types.QueryAccountByAddressResponse{}, nil
		},
	}
	cache := NewAuthzCache(&fakeAuthzRecorder{qc: qc})

	_, err := cache.GetPubKeys(context.Background(), "granter", "msg")
	require.Error(t, err)
}
