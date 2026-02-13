package cosmosclient

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	googleproto "google.golang.org/protobuf/proto"
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
	if !isSupportedProtoMessage(args) {
		return c.inner.Invoke(ctx, method, args, reply, opts...)
	}
	if !isSupportedProtoMessage(reply) {
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
	key := buildCacheKey(method, args, height)
	cached, hit := c.cache.entries[key]
	c.cache.mu.RUnlock()

	if hit {
		if header := headerAddrFromCallOptions(opts); header != nil {
			*header = metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))
		}
		return unmarshalProtoMessage(cached, reply)
	}

	pinnedCtx := ctx
	if explicitHeight == 0 {
		pinnedCtx = setPinnedHeight(ctx, height)
	}

	callOpts := make([]grpc.CallOption, len(opts))
	copy(callOpts, opts)
	responseHeader := headerAddrFromCallOptions(callOpts)
	if responseHeader == nil {
		md := metadata.MD{}
		responseHeader = &md
		callOpts = append(callOpts, grpc.Header(responseHeader))
	}

	if err := c.inner.Invoke(pinnedCtx, method, args, reply, callOpts...); err != nil {
		return err
	}

	responseHeight := heightFromIncomingMD(*responseHeader)
	if responseHeight == 0 {
		return nil
	}

	if data, err := marshalProtoMessage(reply); err == nil {
		cacheKey := buildCacheKey(method, args, responseHeight)
		c.cache.mu.Lock()
		if c.cache.height == responseHeight {
			c.cache.entries[cacheKey] = data
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

func heightFromIncomingMD(md metadata.MD) int64 {
	vals := md.Get(grpctypes.GRPCBlockHeightHeader)
	if len(vals) == 0 {
		return 0
	}
	h, err := strconv.ParseInt(vals[0], 10, 64)
	if err != nil {
		return 0
	}
	return h
}

// headerAddrFromCallOptions finds existing grpc.Header option in opts to avoid adding a duplicate
func headerAddrFromCallOptions(opts []grpc.CallOption) *metadata.MD {
	var headerAddr *metadata.MD
	for _, opt := range opts {
		switch h := opt.(type) {
		case grpc.HeaderCallOption:
			if h.HeaderAddr != nil {
				headerAddr = h.HeaderAddr
			}
		case *grpc.HeaderCallOption:
			if h != nil && h.HeaderAddr != nil {
				headerAddr = h.HeaderAddr
			}
		}
	}
	return headerAddr
}

func (c *CachingConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.inner.NewStream(ctx, desc, method, opts...)
}

func buildCacheKey(method string, msg interface{}, height int64) string {
	data, err := marshalProtoMessage(msg)
	if err != nil {
		return fmt.Sprintf("%s|%d", method, height)
	}
	return fmt.Sprintf("%s|%d|%x", method, height, data)
}

func isSupportedProtoMessage(msg interface{}) bool {
	switch msg.(type) {
	case googleproto.Message, gogoproto.Message:
		return true
	default:
		return false
	}
}

func marshalProtoMessage(msg interface{}) ([]byte, error) {
	switch m := msg.(type) {
	case googleproto.Message:
		return googleproto.Marshal(m)
	case gogoproto.Message:
		return gogoproto.Marshal(m)
	default:
		return nil, fmt.Errorf("unsupported proto message type: %T", msg)
	}
}

func unmarshalProtoMessage(data []byte, msg interface{}) error {
	switch m := msg.(type) {
	case googleproto.Message:
		return googleproto.Unmarshal(data, m)
	case gogoproto.Message:
		return gogoproto.Unmarshal(data, m)
	default:
		return fmt.Errorf("unsupported proto message type: %T", msg)
	}
}
