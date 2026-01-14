package cosmosclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

const (
	MaxCacheEntries = 1000
	CacheTTL        = 30 * time.Second
)

var (
	cacheableQueriesMu sync.RWMutex
	cacheableQueries   = map[string]bool{
		"/inference.inference.Query/Params":                           true,
		"/inference.inference.Query/ModelsAll":                        true,
		"/inference.inference.Query/CurrentEpochGroupData":            true,
		"/inference.inference.Query/EpochGroupData":                   true,
		"/inference.inference.Query/GetAllModelCapacities":            true,
		"/inference.inference.Query/GetAllModelPerTokenPrices":        true,
		"/inference.inference.Query/EpochInfo":                        true,
		"/inference.inference.Query/GetCurrentEpoch":                  true,
		"/inference.inference.Query/HardwareNodesAll":                 true,
		"/inference.inference.Query/InferencesAndTokensStatsByModels": true,
		"/inference.inference.Query/ExcludedParticipants":             true,
		"/inference.inference.Query/CountParticipants":                true,
	}
)

var (
	grpcCache       = expirable.NewLRU[string, []byte](MaxCacheEntries, nil, CacheTTL)
	sfGroup         singleflight.Group
	cacheGeneration atomic.Uint64
)

func IsCacheable(method string) bool {
	cacheableQueriesMu.RLock()
	defer cacheableQueriesMu.RUnlock()
	return cacheableQueries[method]
}

func SetCacheable(method string, cacheable bool) {
	cacheableQueriesMu.Lock()
	defer cacheableQueriesMu.Unlock()
	if cacheable {
		cacheableQueries[method] = true
	} else {
		delete(cacheableQueries, method)
	}
}

func ClearCache() {
	cacheGeneration.Add(1)
	grpcCache.Purge()
}

func cacheKey(method string, req proto.Message) (string, error) {
	data, err := proto.Marshal(req)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return method + ":" + hex.EncodeToString(hash[:16]), nil
}

func CachedInvoke(ctx context.Context, conn grpc.ClientConnInterface, method string, req, reply proto.Message) error {
	if !IsCacheable(method) {
		return conn.Invoke(ctx, method, req, reply)
	}

	key, err := cacheKey(method, req)
	if err != nil {
		return conn.Invoke(ctx, method, req, reply)
	}

	if data, ok := grpcCache.Get(key); ok {
		return proto.Unmarshal(data, reply)
	}

	result, err, _ := sfGroup.Do(key, func() (any, error) {
		if data, ok := grpcCache.Get(key); ok {
			return data, nil
		}

		startGen := cacheGeneration.Load()

		err := conn.Invoke(ctx, method, req, reply)
		if err != nil {
			return nil, err
		}

		data, err := proto.Marshal(reply)
		if err != nil {
			return nil, err
		}

		if cacheGeneration.Load() == startGen {
			grpcCache.Add(key, data)
		}
		return data, nil
	})

	if err != nil {
		return err
	}

	if data, ok := result.([]byte); ok && data != nil {
		return proto.Unmarshal(data, reply)
	}
	return nil
}

type CachingConn struct {
	inner grpc.ClientConnInterface
}

func NewCachingConn(inner grpc.ClientConnInterface) *CachingConn {
	return &CachingConn{inner: inner}
}

func (c *CachingConn) Invoke(ctx context.Context, method string, args any, reply any, opts ...grpc.CallOption) error {
	reqMsg, reqOk := args.(proto.Message)
	replyMsg, replyOk := reply.(proto.Message)

	if reqOk && replyOk {
		return CachedInvoke(ctx, c.inner, method, reqMsg, replyMsg)
	}
	return c.inner.Invoke(ctx, method, args, reply, opts...)
}

func (c *CachingConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.inner.NewStream(ctx, desc, method, opts...)
}
