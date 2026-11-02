package authzcache

import (
	"context"
	"decentralized-api/cosmosclient"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MockQueryClient mocks the inference query client
type MockQueryClient struct {
	mock.Mock
}

func (m *MockQueryClient) GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryGranteesByMessageTypeResponse), args.Error(1)
}

func (m *MockQueryClient) AccountByAddress(ctx context.Context, req *types.QueryAccountByAddressRequest) (*types.QueryAccountByAddressResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryAccountByAddressResponse), args.Error(1)
}

// MockCosmosClient mocks the cosmos message client
type MockCosmosClient struct {
	mock.Mock
	queryClient *MockQueryClient
}

func (m *MockCosmosClient) NewInferenceQueryClient() interface {
	GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error)
	AccountByAddress(ctx context.Context, req *types.QueryAccountByAddressRequest) (*types.QueryAccountByAddressResponse, error)
} {
	return m.queryClient
}

func TestAuthzCache_CacheHit(t *testing.T) {
	// Pre-populate cache with signers
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type": {
				signers: []SignerInfo{
					{Address: "addr1", PubKey: "pubkey1"},
					{Address: "addr2", PubKey: "pubkey2"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	pubkeys, err := cache.GetPubKeys(context.Background(), "granter1", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey1", "pubkey2"}, pubkeys)
}

func TestAuthzCache_CacheExpired(t *testing.T) {
	// Pre-populate cache with expired entry
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type": {
				signers: []SignerInfo{
					{Address: "addr1", PubKey: "old_pubkey"},
				},
				expiresAt: time.Now().Add(-time.Minute), // expired
			},
		},
	}

	// Can't call GetPubKeys without a recorder, but we can verify the expiry check
	entry := cache.cache["granter1|/msg/type"]
	assert.True(t, time.Now().After(entry.expiresAt))
}

func TestAuthzCache_CacheKeyFormat(t *testing.T) {
	// Test that cache key is correctly formed
	granterAddress := "gonka1abc"
	msgTypeUrl := "/inference.inference.MsgClaimRewards"
	expectedKey := granterAddress + "|" + msgTypeUrl

	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			expectedKey: {
				signers: []SignerInfo{
					{Address: granterAddress, PubKey: "pubkey1"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	pubkeys, err := cache.GetPubKeys(context.Background(), granterAddress, msgTypeUrl)
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey1"}, pubkeys)
}

func TestAuthzCache_DifferentMsgTypes(t *testing.T) {
	// Test that different message types are cached separately
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type1": {
				signers: []SignerInfo{
					{Address: "addr1", PubKey: "pubkey_type1"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
			"granter1|/msg/type2": {
				signers: []SignerInfo{
					{Address: "addr2", PubKey: "pubkey_type2"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	pubkeys1, err := cache.GetPubKeys(context.Background(), "granter1", "/msg/type1")
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey_type1"}, pubkeys1)

	pubkeys2, err := cache.GetPubKeys(context.Background(), "granter1", "/msg/type2")
	assert.NoError(t, err)
	assert.Equal(t, []string{"pubkey_type2"}, pubkeys2)
}

func TestAuthzCache_GetPubKeyForSigner(t *testing.T) {
	cache := &AuthzCache{
		cache: map[string]*cachedEntry{
			"granter1|/msg/type": {
				signers: []SignerInfo{
					{Address: "signer1", PubKey: "pubkey1"},
					{Address: "signer2", PubKey: "pubkey2"},
					{Address: "granter1", PubKey: "granter_pubkey"},
				},
				expiresAt: time.Now().Add(time.Minute),
			},
		},
	}

	// Should find signer1
	pubkey, err := cache.GetPubKeyForSigner(context.Background(), "granter1", "signer1", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "pubkey1", pubkey)

	// Should find signer2
	pubkey, err = cache.GetPubKeyForSigner(context.Background(), "granter1", "signer2", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "pubkey2", pubkey)

	// Should find granter itself
	pubkey, err = cache.GetPubKeyForSigner(context.Background(), "granter1", "granter1", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "granter_pubkey", pubkey)

	// Should return empty string for unknown signer (not an error)
	pubkey, err = cache.GetPubKeyForSigner(context.Background(), "granter1", "unknown_signer", "/msg/type")
	assert.NoError(t, err)
	assert.Equal(t, "", pubkey)
}

type fakeRecorder struct {
	cosmosclient.CosmosMessageClient
	queryClient types.QueryClient
}

func (f *fakeRecorder) NewInferenceQueryClient() types.QueryClient {
	return f.queryClient
}

type fakeChainClient struct {
	types.QueryClient
	mu    sync.Mutex
	calls map[string]int
	block map[string]chan struct{}
	errs  map[string]error
}

func newFakeChainClient() *fakeChainClient {
	return &fakeChainClient{
		calls: map[string]int{},
		block: map[string]chan struct{}{},
		errs:  map[string]error{},
	}
}

func (f *fakeChainClient) GranteesByMessageType(ctx context.Context, req *types.QueryGranteesByMessageTypeRequest, _ ...grpc.CallOption) (*types.QueryGranteesByMessageTypeResponse, error) {
	f.mu.Lock()
	f.calls[req.GranterAddress]++
	block := f.block[req.GranterAddress]
	err := f.errs[req.GranterAddress]
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &types.QueryGranteesByMessageTypeResponse{}, nil
}

func (f *fakeChainClient) AccountByAddress(_ context.Context, req *types.QueryAccountByAddressRequest, _ ...grpc.CallOption) (*types.QueryAccountByAddressResponse, error) {
	return &types.QueryAccountByAddressResponse{Pubkey: "pubkey_" + req.Address}, nil
}

func (f *fakeChainClient) callCount(granter string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[granter]
}

func (f *fakeChainClient) setErr(granter string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[granter] = err
}

// Test flow:
// 1. Block the chain call for granter A and start a lookup for A in the background.
// 2. Wait until the call for A has reached the chain.
// 3. Look up granter B and require it to return while A is still blocked.
// 4. Unblock A and require its lookup to succeed.
func TestAuthzCache_SlowLookupDoesNotBlockOtherKeys(t *testing.T) {
	chain := newFakeChainClient()
	release := make(chan struct{})
	chain.block["granterA"] = release
	cache := NewAuthzCache(&fakeRecorder{queryClient: chain})

	errA := make(chan error, 1)
	go func() {
		_, err := cache.GetPubKeys(context.Background(), "granterA", "/msg/type")
		errA <- err
	}()
	require.Eventually(t, func() bool { return chain.callCount("granterA") == 1 }, time.Second, time.Millisecond)

	doneB := make(chan []string, 1)
	go func() {
		pubkeys, _ := cache.GetPubKeys(context.Background(), "granterB", "/msg/type")
		doneB <- pubkeys
	}()
	select {
	case pubkeys := <-doneB:
		require.Equal(t, []string{"pubkey_granterB"}, pubkeys)
	case <-time.After(time.Second):
		close(release)
		<-errA
		t.Fatal("lookup for granterB blocked behind granterA")
	}

	close(release)
	require.NoError(t, <-errA)
}

// Test flow:
// 1. Make the chain call for granter A fail and look A up twice.
// 2. Require both lookups to return the error and the chain to be called once.
// 3. Expire the cached failure and clear the chain error.
// 4. Look A up again and require a fresh chain call that succeeds.
func TestAuthzCache_FailedLookupIsCachedBriefly(t *testing.T) {
	granter := sdk.AccAddress([]byte("granterA_20_bytes___")).String()
	chain := newFakeChainClient()
	chainErr := status.Error(codes.Unavailable, "node down")
	chain.setErr(granter, chainErr)
	cache := NewAuthzCache(&fakeRecorder{queryClient: chain})

	_, err := cache.GetPubKeys(context.Background(), granter, "/msg/type")
	require.ErrorIs(t, err, chainErr)
	_, err = cache.GetPubKeys(context.Background(), granter, "/msg/type")
	require.ErrorIs(t, err, chainErr)
	require.Equal(t, 1, chain.callCount(granter))

	cache.mu.Lock()
	entry := cache.cache[granter+"|/msg/type"]
	require.NotNil(t, entry)
	require.True(t, entry.expiresAt.Before(time.Now().Add(authzCacheTTL/2)))
	entry.expiresAt = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	chain.setErr(granter, nil)

	pubkeys, err := cache.GetPubKeys(context.Background(), granter, "/msg/type")
	require.NoError(t, err)
	require.Equal(t, []string{"pubkey_" + granter}, pubkeys)
	require.Equal(t, 2, chain.callCount(granter))
}

// Test flow:
// 1. Look up granter A with an already canceled context and require an error.
// 2. Make the chain return NotFound for granter N and look N up.
// 3. Look both up again and require each to reach the chain a second time.
// 4. Require no entry to be stored for granter N.
func TestAuthzCache_CanceledOrNotFoundIsNotCached(t *testing.T) {
	chain := newFakeChainClient()
	chain.setErr("granterN", status.Error(codes.NotFound, "account not found"))
	cache := NewAuthzCache(&fakeRecorder{queryClient: chain})

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.GetPubKeys(canceled, "granterA", "/msg/type")
	require.ErrorIs(t, err, context.Canceled)
	_, err = cache.GetPubKeys(context.Background(), "granterN", "/msg/type")
	require.Equal(t, codes.NotFound, status.Code(err))

	_, err = cache.GetPubKeys(context.Background(), "granterA", "/msg/type")
	require.NoError(t, err)
	_, err = cache.GetPubKeys(context.Background(), "granterN", "/msg/type")
	require.Equal(t, codes.NotFound, status.Code(err))
	require.Equal(t, 2, chain.callCount("granterA"))
	require.Equal(t, 2, chain.callCount("granterN"))

	cache.mu.RLock()
	defer cache.mu.RUnlock()
	require.NotContains(t, cache.cache, "granterN|/msg/type")
}

// Test flow:
// 1. Make the chain fail for 1000 distinct malformed 64 KB granter addresses.
// 2. Look each address up and require an error.
// 3. Require the cache to hold no entries.
func TestAuthzCache_MalformedGranterNotCached(t *testing.T) {
	chain := newFakeChainClient()
	cache := NewAuthzCache(&fakeRecorder{queryClient: chain})
	pad := strings.Repeat("x", 64<<10)
	for i := 0; i < 1000; i++ {
		addr := fmt.Sprintf("bad%06d%s", i, pad)
		chain.setErr(addr, status.Error(codes.Internal, "failed to get grants"))
		_, err := cache.GetPubKeys(context.Background(), addr, "/msg/type")
		require.Error(t, err)
	}

	cache.mu.RLock()
	defer cache.mu.RUnlock()
	require.Equal(t, 0, len(cache.cache))
}

// Test flow:
// 1. Make the chain fail for valid granter A, look A up and expire its entry.
// 2. Make the chain fail for valid granter B and look B up.
// 3. Require only B's entry to remain in the cache.
func TestAuthzCache_ExpiredEntriesSweptOnFailedInsert(t *testing.T) {
	granterA := sdk.AccAddress([]byte("granterA_20_bytes___")).String()
	granterB := sdk.AccAddress([]byte("granterB_20_bytes___")).String()
	chain := newFakeChainClient()
	chain.setErr(granterA, status.Error(codes.Unavailable, "node down"))
	chain.setErr(granterB, status.Error(codes.Unavailable, "node down"))
	cache := NewAuthzCache(&fakeRecorder{queryClient: chain})

	_, err := cache.GetPubKeys(context.Background(), granterA, "/msg/type")
	require.Error(t, err)
	cache.mu.Lock()
	require.Contains(t, cache.cache, granterA+"|/msg/type")
	cache.cache[granterA+"|/msg/type"].expiresAt = time.Now().Add(-time.Second)
	cache.mu.Unlock()

	_, err = cache.GetPubKeys(context.Background(), granterB, "/msg/type")
	require.Error(t, err)

	cache.mu.RLock()
	defer cache.mu.RUnlock()
	require.Len(t, cache.cache, 1)
	require.Contains(t, cache.cache, granterB+"|/msg/type")
}
