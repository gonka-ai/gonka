package cosmosclient

import (
	"context"
	"strconv"
	"testing"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

type testConn struct {
	height     int64
	invokes    int
	invokeErr  error
	headerOpts int
}

func (c *testConn) Invoke(_ context.Context, _ string, _ interface{}, _ interface{}, opts ...grpc.CallOption) error {
	c.invokes++
	if c.invokeErr != nil {
		return c.invokeErr
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
	c.headerOpts = len(headerAddrs)
	if len(headerAddrs) > 0 {
		*headerAddrs[len(headerAddrs)-1] = metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(c.height, 10))
	}
	return nil
}

func (c *testConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func TestCachingConn_CachesByResponseHeight(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeight(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 1, inner.invokes)

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 1, inner.invokes)
}

func TestCachingConn_DoesNotCacheWhenResponseHeightDiffers(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeight(100)

	inner := &testConn{height: 99}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp))
	require.Equal(t, 2, inner.invokes)
}

func TestCachingConn_UsesExistingHeaderCallOption(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeight(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &emptypb.Empty{}
	resp := &emptypb.Empty{}
	callerHeader := metadata.MD{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp, grpc.Header(&callerHeader)))
	require.Equal(t, 1, inner.headerOpts)
	require.Equal(t, []string{"100"}, callerHeader.Get(grpctypes.GRPCBlockHeightHeader))

	cachedHeader := metadata.MD{}
	require.NoError(t, conn.Invoke(context.Background(), "/inference.Query/EpochInfo", req, resp, grpc.Header(&cachedHeader)))
	require.Equal(t, 1, inner.invokes)
	require.Equal(t, []string{"100"}, cachedHeader.Get(grpctypes.GRPCBlockHeightHeader))
}

func TestCachingConn_SupportsGogoProtoMessages(t *testing.T) {
	cache := NewQueryCache()
	cache.SetHeight(100)

	inner := &testConn{height: 100}
	conn := &CachingConn{inner: inner, cache: cache}

	req := &inferencetypes.QueryParamsRequest{}
	resp := &inferencetypes.QueryParamsResponse{}

	require.NoError(t, conn.Invoke(context.Background(), "/inference.inference.Query/Params", req, resp))
	require.Equal(t, 1, inner.invokes)

	require.NoError(t, conn.Invoke(context.Background(), "/inference.inference.Query/Params", req, resp))
	require.Equal(t, 1, inner.invokes)
}
