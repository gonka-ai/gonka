package cosmosclient

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

type testConn struct {
	mu            sync.Mutex
	height        int64
	invokes       int
	invokeErr     error
	headerOpts    int
	blockCh       chan struct{}
	startedCh     chan struct{}
	blockOnce     bool
	heightFromCtx bool
	omitHeader    bool
}

func (c *testConn) Invoke(ctx context.Context, _ string, _ interface{}, _ interface{}, opts ...grpc.CallOption) error {
	c.mu.Lock()
	c.invokes++
	currentInvoke := c.invokes
	invokeErr := c.invokeErr
	height := c.height
	blockCh := c.blockCh
	startedCh := c.startedCh
	blockOnce := c.blockOnce
	heightFromCtx := c.heightFromCtx
	omitHeader := c.omitHeader
	c.mu.Unlock()

	if heightFromCtx {
		if pinned := heightFromOutgoingCtx(ctx); pinned > 0 {
			height = pinned
		}
	}

	if startedCh != nil {
		select {
		case startedCh <- struct{}{}:
		default:
		}
	}
	if blockCh != nil && (!blockOnce || currentInvoke == 1) {
		select {
		case <-blockCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if invokeErr != nil {
		return invokeErr
	}

	var headerAddrs []*metadata.MD
	for _, opt := range opts {
		switch headerOpt := opt.(type) {
		case grpc.HeaderCallOption:
			if headerOpt.HeaderAddr != nil {
				headerAddrs = append(headerAddrs, headerOpt.HeaderAddr)
			}
		case *grpc.HeaderCallOption:
			if headerOpt != nil && headerOpt.HeaderAddr != nil {
				headerAddrs = append(headerAddrs, headerOpt.HeaderAddr)
			}
		}
	}
	c.mu.Lock()
	c.headerOpts = len(headerAddrs)
	c.mu.Unlock()
	if len(headerAddrs) > 0 && !omitHeader {
		*headerAddrs[len(headerAddrs)-1] = metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))
	}
	return nil
}

func (c *testConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func (c *testConn) invokeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invokes
}

func (c *testConn) headerOptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headerOpts
}

func TestCachingConn_CachesByResponseHeight(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 1, inner.invokeCount())

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 1, inner.invokeCount())
}

func TestCachingConn_CachesAtResponseHeightWhenItDiffers(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{height: 99}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 2, inner.invokeCount())
}

func TestCachingConn_UsesExistingHeaderCallOption(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}
	callerHeader := metadata.MD{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp, grpc.Header(&callerHeader)))
	require.Equal(t, 1, inner.headerOptCount())
	require.Equal(t, []string{"100"}, callerHeader.Get(grpctypes.GRPCBlockHeightHeader))

	cachedHeader := metadata.MD{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp, grpc.Header(&cachedHeader)))
	require.Equal(t, 1, inner.invokeCount())
	require.Equal(t, []string{"100"}, cachedHeader.Get(grpctypes.GRPCBlockHeightHeader))
}

func TestCachingConn_SupportsGogoProtoMessages(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &inferencetypes.QueryParamsRequest{}
	resp := &inferencetypes.QueryParamsResponse{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.inference.Query/Params", req, resp))
	require.Equal(t, 1, inner.invokeCount())

	require.NoError(t, conn.Invoke(context.Background(), "/inference.inference.Query/Params", req, resp))
	require.Equal(t, 1, inner.invokeCount())
}

func TestCachingConn_DeduplicatesConcurrentMisses(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{
		height:    100,
		blockCh:   make(chan struct{}),
		startedCh: make(chan struct{}, 16),
	}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}

	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := &emptypb.Empty{}
			errCh <- conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp)
		}()
	}

	<-inner.startedCh
	close(inner.blockCh)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	require.Equal(t, 1, inner.invokeCount())
	stats := cache.SnapshotStats()
	require.Equal(t, uint64(workers), stats.RequestsTotal)
	require.GreaterOrEqual(t, stats.CacheMissTotal, uint64(1))
	require.Equal(t, uint64(workers), stats.CacheHitTotal+stats.CacheMissTotal)
	require.Equal(t, uint64(1), stats.BackendInvokeTotal)
	require.Equal(t, uint64(1), stats.CacheWriteTotal)
}

func TestCachingConn_DeduplicatesConcurrentMisses_WhenResponseHeightMissing(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{
		height:     100,
		omitHeader: true,
		blockCh:    make(chan struct{}),
		startedCh:  make(chan struct{}, 1),
	}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	const method = "/inference.Query/EpochInfo"

	leaderErrCh := make(chan error, 1)
	go func() {
		resp := &emptypb.Empty{}
		leaderErrCh <- conn.Invoke(context.Background(), method, req, resp)
	}()

	<-inner.startedCh

	const workers = 8
	startFollowers := make(chan struct{})
	followersReady := make(chan struct{}, workers)
	followerErrCh := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := &emptypb.Empty{}
			followersReady <- struct{}{}
			<-startFollowers
			followerErrCh <- conn.Invoke(context.Background(), method, req, resp)
		}()
	}

	for i := 0; i < workers; i++ {
		<-followersReady
	}
	close(startFollowers)

	require.Eventually(t, func() bool {
		return cache.SnapshotStats().RequestsTotal == uint64(workers+1)
	}, time.Second, time.Millisecond)

	close(inner.blockCh)

	require.NoError(t, <-leaderErrCh)
	wg.Wait()
	close(followerErrCh)

	for err := range followerErrCh {
		require.NoError(t, err)
	}

	require.Equal(t, 1, inner.invokeCount())
	stats := cache.SnapshotStats()
	require.Equal(t, uint64(workers+1), stats.RequestsTotal)
	require.Equal(t, uint64(1), stats.BackendInvokeTotal)
	require.Equal(t, uint64(0), stats.CacheWriteTotal)
	require.Equal(t, uint64(1), stats.CacheWriteSkippedHeightTotal)
	require.Equal(t, 0, stats.Heights)
	require.Equal(t, 0, stats.Entries)
}

func TestCachingConn_WithoutQueryCache_BypassesHintAndCache(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	ctx := WithoutQueryCache(context.Background())

	resp := &emptypb.Empty{}
	require.NoError(t, conn.Invoke(ctx, "/inference.Query/EpochInfo", req, resp))
	require.NoError(t, conn.Invoke(ctx, "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 2, inner.invokeCount())

	stats := cache.SnapshotStats()
	require.Equal(t, uint64(2), stats.RequestsTotal)
	require.Equal(t, uint64(0), stats.CacheHitTotal)
	require.Equal(t, uint64(0), stats.CacheMissTotal)
	require.Equal(t, uint64(2), stats.BackendInvokeTotal)
	require.Equal(t, uint64(0), stats.CacheWriteTotal)
}

func TestCachingConn_FallbacksToBackendOnCorruptedCacheEntry(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	req := &emptypb.Empty{}
	requestHash, err := buildRequestHash(req)
	require.NoError(t, err)
	key := buildCacheKey("/inference.Query/EpochInfo", requestHash)
	cache.store(100, key, []byte("corrupted-payload"))

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}
	resp := &emptypb.Empty{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 1, inner.invokeCount())
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 1, inner.invokeCount())

	stats := cache.SnapshotStats()
	require.Equal(t, uint64(1), stats.CacheHitTotal)
	require.Equal(t, uint64(1), stats.CacheCorruptHitTotal)
	require.Equal(t, uint64(1), stats.CacheMissTotal)
	require.Equal(t, uint64(1), stats.BackendInvokeTotal)
	require.Equal(t, uint64(1), stats.CacheWriteTotal)
}

func TestQueryCacheStatsReset(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))

	beforeReset := cache.SnapshotStats()
	require.Equal(t, uint64(2), beforeReset.RequestsTotal)
	require.Equal(t, uint64(1), beforeReset.CacheMissTotal)
	require.Equal(t, uint64(1), beforeReset.CacheHitTotal)
	require.Equal(t, uint64(0), beforeReset.CacheCorruptHitTotal)
	require.Equal(t, uint64(1), beforeReset.BackendInvokeTotal)
	require.Equal(t, uint64(1), beforeReset.CacheWriteTotal)
	require.Equal(t, int64(100), beforeReset.HeightHint)
	require.Equal(t, 1, beforeReset.Heights)
	require.Equal(t, 1, beforeReset.Entries)

	cache.ResetStats()

	afterReset := cache.SnapshotStats()
	require.Equal(t, uint64(0), afterReset.RequestsTotal)
	require.Equal(t, uint64(0), afterReset.CacheMissTotal)
	require.Equal(t, uint64(0), afterReset.CacheHitTotal)
	require.Equal(t, uint64(0), afterReset.CacheCorruptHitTotal)
	require.Equal(t, uint64(0), afterReset.BackendInvokeTotal)
	require.Equal(t, uint64(0), afterReset.CacheWriteTotal)
	require.Equal(t, uint64(0), afterReset.InvokeErrorTotal)
	require.Equal(t, int64(100), afterReset.HeightHint)
	require.Equal(t, 1, afterReset.Heights)
	require.Equal(t, 1, afterReset.Entries)
}

func TestCachingConn_RetriesBackendForSharedContextCancellation(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{
		height:    100,
		blockCh:   make(chan struct{}),
		startedCh: make(chan struct{}, 2),
		blockOnce: true,
	}
	conn := &CachingConn{inner: inner, cache: cache}
	req := &emptypb.Empty{}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	var (
		leaderErr   error
		followerErr error
	)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		resp := &emptypb.Empty{}
		leaderErr = conn.Invoke(leaderCtx, "/inference.Query/EpochInfo", req, resp)
	}()

	<-inner.startedCh

	go func() {
		defer wg.Done()
		resp := &emptypb.Empty{}
		followerErr = conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp)
	}()

	cancelLeader()
	wg.Wait()

	require.ErrorIs(t, leaderErr, context.Canceled)
	require.NoError(t, followerErr)
	require.Equal(t, 2, inner.invokeCount())

	stats := cache.SnapshotStats()
	require.Equal(t, uint64(2), stats.RequestsTotal)
	require.Equal(t, uint64(2), stats.CacheMissTotal)
	require.Equal(t, uint64(2), stats.BackendInvokeTotal)
	require.Equal(t, uint64(1), stats.InvokeErrorTotal)
	require.Equal(t, uint64(1), stats.CacheWriteTotal)
}

func TestCachingConn_SharedCancellationRetryDoesNotStampedeBackend(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{
		height:    100,
		blockCh:   make(chan struct{}),
		startedCh: make(chan struct{}, 4),
		blockOnce: true,
	}
	conn := &CachingConn{inner: inner, cache: cache}
	req := &emptypb.Empty{}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	const followers = 8
	var leaderErr error
	followerErrCh := make(chan error, followers)

	startFollowers := make(chan struct{})
	followersReady := make(chan struct{}, followers)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp := &emptypb.Empty{}
		leaderErr = conn.Invoke(leaderCtx, "/inference.Query/EpochInfo", req, resp)
	}()

	<-inner.startedCh

	for i := 0; i < followers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := &emptypb.Empty{}
			followersReady <- struct{}{}
			<-startFollowers
			followerErrCh <- conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp)
		}()
	}

	for i := 0; i < followers; i++ {
		<-followersReady
	}
	close(startFollowers)

	cancelLeader()
	wg.Wait()
	close(followerErrCh)

	require.ErrorIs(t, leaderErr, context.Canceled)
	for err := range followerErrCh {
		require.NoError(t, err)
	}

	require.Equal(t, 2, inner.invokeCount())
	stats := cache.SnapshotStats()
	require.Equal(t, uint64(followers+1), stats.RequestsTotal)
	require.Equal(t, uint64(2), stats.BackendInvokeTotal)
	require.Equal(t, uint64(1), stats.InvokeErrorTotal)
	require.Equal(t, uint64(1), stats.CacheWriteTotal)
}

func TestCachingConn_TwoWorkersDifferentBlockCtx_NoInterference(t *testing.T) {
	cache := NewQueryCache()
	inner := &testConn{heightFromCtx: true}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	const method = "/inference.Query/EpochInfo"

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx := PinHeight(context.Background(), 100)
		resp := &emptypb.Empty{}
		require.NoError(t, conn.Invoke(ctx, method, req, resp))
		require.NoError(t, conn.Invoke(ctx, method, req, resp)) // hit on 100
	}()
	go func() {
		defer wg.Done()
		ctx := PinHeight(context.Background(), 101)
		resp := &emptypb.Empty{}
		require.NoError(t, conn.Invoke(ctx, method, req, resp))
		require.NoError(t, conn.Invoke(ctx, method, req, resp)) // hit on 101
	}()
	wg.Wait()

	require.Equal(t, 2, inner.invokeCount(), "exactly one backend call per height")

	stats := cache.SnapshotStats()
	require.Equal(t, uint64(4), stats.RequestsTotal)
	require.Equal(t, uint64(2), stats.CacheHitTotal)
	require.Equal(t, uint64(2), stats.CacheMissTotal)
	require.Equal(t, uint64(2), stats.CacheWriteTotal)
	require.Equal(t, 2, stats.Heights)
	require.Equal(t, 2, stats.Entries)

	requestHash, err := buildRequestHash(req)
	require.NoError(t, err)
	key := buildCacheKey(method, requestHash)
	_, ok := cache.lookup(100, key)
	require.True(t, ok, "height 100 entry must exist")
	_, ok = cache.lookup(101, key)
	require.True(t, ok, "height 101 entry must exist")
}

func TestCachingConn_HintFallbackForUnpinnedCallers(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(200)

	inner := &testConn{height: 200}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", req, resp))
	require.Equal(t, 1, inner.invokeCount())

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", req, resp))
	require.Equal(t, 1, inner.invokeCount(), "second unpinned call must hit cache via hint")
}

func TestCachingConn_HintFlipDuringRequest_NoCorruption(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &testConn{
		heightFromCtx: true,
		blockCh:       make(chan struct{}),
		startedCh:     make(chan struct{}, 2),
		blockOnce:     true,
	}
	conn := &CachingConn{inner: inner, cache: cache}
	req := &emptypb.Empty{}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := PinHeight(context.Background(), 100)
		resp := &emptypb.Empty{}
		require.NoError(t, conn.Invoke(ctx, "/inference.Query/EpochInfo", req, resp))
	}()

	<-inner.startedCh
	cache.SetHeightHint(101)
	close(inner.blockCh)
	wg.Wait()

	require.Equal(t, int64(101), cache.HeightHint(), "hint must not be lowered by completing request")

	requestHash, err := buildRequestHash(req)
	require.NoError(t, err)
	key := buildCacheKey("/inference.Query/EpochInfo", requestHash)
	_, hit100 := cache.lookup(100, key)
	require.True(t, hit100, "in-flight request must have cached at its actual response height (100)")
	_, hit101 := cache.lookup(101, key)
	require.False(t, hit101, "no entry must exist at the bumped hint height")

	resp := &emptypb.Empty{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 2, inner.invokeCount(), "post-flip call must reach backend, not reuse 100 entry")
}

func TestQueryCachePruning_KeepsLastNHeights(t *testing.T) {
	cache := NewQueryCache()
	inner := &testConn{heightFromCtx: true}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	const method = "/inference.Query/EpochInfo"

	for h := int64(1); h <= int64(defaultKeepLastHeights+2); h++ {
		ctx := PinHeight(context.Background(), h)
		resp := &emptypb.Empty{}
		require.NoError(t, conn.Invoke(ctx, method, req, resp))
	}

	stats := cache.SnapshotStats()
	require.Equal(t, defaultKeepLastHeights, stats.Heights)

	requestHash, err := buildRequestHash(req)
	require.NoError(t, err)
	key := buildCacheKey(method, requestHash)

	_, ok := cache.lookup(1, key)
	require.False(t, ok, "oldest height should have been pruned")
	_, ok = cache.lookup(2, key)
	require.False(t, ok, "second oldest height should have been pruned")
	for h := int64(3); h <= int64(defaultKeepLastHeights+2); h++ {
		_, ok := cache.lookup(h, key)
		require.Truef(t, ok, "height %d entry must remain", h)
	}
}

func TestPinHeight_DoesNotStackDuplicateValues(t *testing.T) {
	ctx := PinHeight(context.Background(), 100)
	ctx = PinHeight(ctx, 100)
	require.Equal(t, int64(100), heightFromOutgoingCtx(ctx))

	md, ok := metadata.FromOutgoingContext(ctx)
	require.True(t, ok)
	vals := md.Get(grpctypes.GRPCBlockHeightHeader)
	require.Equal(t, []string{"100"}, vals, "no duplicate header values for same height")

	ctx = PinHeight(ctx, 101)
	require.Equal(t, int64(101), heightFromOutgoingCtx(ctx))
}
