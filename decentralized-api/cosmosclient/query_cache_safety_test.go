package cosmosclient

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

type latestConn struct {
	mu           sync.Mutex
	latestHeight int64
	invokes      int
	pinnedSeen   []int64
	extraHeader  bool
}

func (c *latestConn) Invoke(ctx context.Context, _ string, _ interface{}, _ interface{}, opts ...grpc.CallOption) error {
	c.mu.Lock()
	c.invokes++
	c.pinnedSeen = append(c.pinnedSeen, heightFromOutgoingCtx(ctx))
	latest := c.latestHeight
	extra := c.extraHeader
	c.mu.Unlock()

	height := latest
	if pinned := heightFromOutgoingCtx(ctx); pinned > 0 {
		height = pinned
	}

	if header := headerAddrFromCallOptions(opts); header != nil {
		md := metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))
		if extra {
			md.Set("x-extra", "backend-value")
		}
		*header = md
	}
	return nil
}

func (c *latestConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func (c *latestConn) invokeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invokes
}

func (c *latestConn) pinned() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.pinnedSeen...)
}

func TestCachingConn_UnpinnedMissIsNotPinnedAndHealsHint(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &latestConn{latestHeight: 101}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}
	header := metadata.MD{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", req, resp, grpc.Header(&header)))

	require.Equal(t, []int64{0}, inner.pinned(), "unpinned caller must not be pinned to the hint")
	require.Equal(t, []string{"101"}, header.Get(grpctypes.GRPCBlockHeightHeader))
	require.Equal(t, int64(101), cache.HeightHint(), "response height must advance the hint")

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", req, resp))
	require.Equal(t, 1, inner.invokeCount())
}

func TestCachingConn_ExplicitPinIsPreserved(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &latestConn{latestHeight: 105}
	conn := &CachingConn{inner: inner, cache: cache}

	ctx := PinHeight(context.Background(), 99)
	require.NoError(t, conn.Invoke(ctx, "/inference.Query/Models", &emptypb.Empty{}, &emptypb.Empty{}))
	require.Equal(t, []int64{99}, inner.pinned())
}

func TestCachingConn_StaleHintBypassesLookupAndSelfHeals(t *testing.T) {
	cache := NewQueryCache()
	cache.maxHintAge = time.Minute
	cache.SetHeightHint(100)

	req := &emptypb.Empty{}
	requestHash, err := buildRequestHash(req)
	require.NoError(t, err)
	key := buildCacheKey("/inference.Query/Models", requestHash)
	cache.store(100, key, []byte{})

	cache.hintSetAt.Store(time.Now().Add(-time.Hour).UnixNano())

	inner := &latestConn{latestHeight: 102}
	conn := &CachingConn{inner: inner, cache: cache}

	resp := &emptypb.Empty{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", req, resp))
	require.Equal(t, 1, inner.invokeCount(), "stale hint must skip the cache and hit the backend")
	require.Equal(t, int64(102), cache.HeightHint())

	stats := cache.SnapshotStats()
	require.Equal(t, uint64(1), stats.StaleHintBypassTotal)

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", req, resp))
	require.Equal(t, 1, inner.invokeCount())
}

func TestCachingConn_HeaderKeepsBackendMetadataOnMiss(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeightHint(100)

	inner := &latestConn{latestHeight: 100, extraHeader: true}
	conn := &CachingConn{inner: inner, cache: cache}

	header := metadata.MD{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", &emptypb.Empty{}, &emptypb.Empty{}, grpc.Header(&header)))
	require.Equal(t, []string{"backend-value"}, header.Get("x-extra"))
	require.Equal(t, []string{"100"}, header.Get(grpctypes.GRPCBlockHeightHeader))

	cachedHeader := metadata.MD{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/Models", &emptypb.Empty{}, &emptypb.Empty{}, grpc.Header(&cachedHeader)))
	require.Equal(t, 1, inner.invokeCount())
	require.Equal(t, []string{"100"}, cachedHeader.Get(grpctypes.GRPCBlockHeightHeader))
}

func TestQueryCache_HeightPruneCountsInStats(t *testing.T) {
	cache := NewQueryCache()

	for h := int64(1); h <= int64(defaultKeepLastHeights+2); h++ {
		cache.store(h, "k", []byte("v"))
	}

	stats := cache.SnapshotStats()
	require.Equal(t, defaultKeepLastHeights, stats.Heights)
	require.Equal(t, uint64(2), stats.CachePruneTotal)
}
