package cosmosclient

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type QueryCache struct {
	mu      sync.RWMutex
	height  int64
	entries map[string][]byte
}

func NewQueryCache() *QueryCache {
	return &QueryCache{entries: make(map[string][]byte)}
}

func (c *QueryCache) SetHeight(h int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h > c.height {
		c.height = h
		c.entries = make(map[string][]byte)
	}
}

type CachingConn struct {
	inner grpc.ClientConnInterface
	cache *QueryCache
}

func (c *CachingConn) Invoke(ctx context.Context, method string, args, reply interface{}, opts ...grpc.CallOption) error {
	reqMsg, ok := args.(proto.Message)
	if !ok {
		return c.inner.Invoke(ctx, method, args, reply, opts...)
	}
	replyMsg, ok := reply.(proto.Message)
	if !ok {
		return c.inner.Invoke(ctx, method, args, reply, opts...)
	}

	explicitHeight := heightFromOutgoingCtx(ctx)

	c.cache.mu.RLock()
	height := c.cache.height
	if explicitHeight > 0 {
		height = explicitHeight
	}
	if height == 0 {
		c.cache.mu.RUnlock()
		return c.inner.Invoke(ctx, method, args, reply, opts...)
	}
	key := buildCacheKey(method, reqMsg, height)
	cached, hit := c.cache.entries[key]
	c.cache.mu.RUnlock()

	if hit {
		return proto.Unmarshal(cached, replyMsg)
	}

	pinnedCtx := ctx
	if explicitHeight == 0 {
		pinnedCtx = setPinnedHeight(ctx, height)
	}

	if err := c.inner.Invoke(pinnedCtx, method, args, reply, opts...); err != nil {
		return err
	}

	if data, err := proto.Marshal(replyMsg); err == nil {
		c.cache.mu.Lock()
		if c.cache.height == height {
			c.cache.entries[key] = data
		}
		c.cache.mu.Unlock()
	}
	return nil
}

func heightFromOutgoingCtx(ctx context.Context) int64 {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return 0
	}
	vals := md.Get(grpctypes.GRPCBlockHeightHeader)
	if len(vals) == 0 {
		return 0
	}
	h, _ := strconv.ParseInt(vals[0], 10, 64)
	return h
}

func setPinnedHeight(ctx context.Context, height int64) context.Context {
	return metadata.AppendToOutgoingContext(ctx,
		grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))
}

func (c *CachingConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.inner.NewStream(ctx, desc, method, opts...)
}

func buildCacheKey(method string, msg proto.Message, height int64) string {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Sprintf("%s|%d", method, height)
	}
	return fmt.Sprintf("%s|%d|%x", method, height, data)
}
