package cosmosclient

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	googleproto "google.golang.org/protobuf/proto"
)

const (
	defaultKeepLastHeights = 3
	defaultMaxEntries = 200000
	defaultMaxBytes   = 1 << 30 // 1 GiB
)

type queryCacheBypassKey struct{}

var bypassQueryCacheKey queryCacheBypassKey

func WithoutQueryCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, bypassQueryCacheKey, true)
}

func shouldBypassQueryCache(ctx context.Context) bool {
	v, ok := ctx.Value(bypassQueryCacheKey).(bool)
	return ok && v
}

type QueryCacheStats struct {
	HeightHint                   int64  `json:"height_hint"`
	Heights                      int    `json:"heights"`
	Entries                      int    `json:"entries"`
	TotalBytes                   int64  `json:"total_bytes"`
	MaxEntries                   int    `json:"max_entries"`
	MaxBytes                     int64  `json:"max_bytes"`
	RequestsTotal                uint64 `json:"requests_total"`
	CacheHitTotal                uint64 `json:"cache_hit_total"`
	CacheCorruptHitTotal         uint64 `json:"cache_corrupt_hit_total"`
	CacheMissTotal               uint64 `json:"cache_miss_total"`
	BackendInvokeTotal           uint64 `json:"backend_invoke_total"`
	CacheWriteTotal              uint64 `json:"cache_write_total"`
	CacheWriteSkippedHeightTotal uint64 `json:"cache_write_skipped_height_total"`
	CacheEvictTotal              uint64 `json:"cache_evict_total"`
	InvokeErrorTotal             uint64 `json:"invoke_error_total"`
}

type queryCacheCounters struct {
	requestsTotal                atomic.Uint64
	cacheHitTotal                atomic.Uint64
	cacheCorruptHitTotal         atomic.Uint64
	cacheMissTotal               atomic.Uint64
	backendInvokeTotal           atomic.Uint64
	cacheWriteTotal              atomic.Uint64
	cacheWriteSkippedHeightTotal atomic.Uint64
	cacheEvictTotal              atomic.Uint64
	invokeErrorTotal             atomic.Uint64
}

type lruKey struct {
	height int64
	key    string
}

type lruEntry struct {
	height int64
	key    string
	size   int
}

type QueryCache struct {
	hint atomic.Int64

	mu       sync.Mutex
	byHeight map[int64]map[string][]byte
	keepLast int

	lru        *list.List
	lruIndex   map[lruKey]*list.Element
	totalBytes int64
	maxEntries int
	maxBytes   int64

	sfGroup singleflight.Group
	stats   queryCacheCounters
}

func NewQueryCache() *QueryCache {
	return NewQueryCacheWithLimits(defaultMaxEntries, defaultMaxBytes)
}

func NewQueryCacheWithLimits(maxEntries int, maxBytes int64) *QueryCache {
	return &QueryCache{
		byHeight:   make(map[int64]map[string][]byte),
		keepLast:   defaultKeepLastHeights,
		lru:        list.New(),
		lruIndex:   make(map[lruKey]*list.Element),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
	}
}

func (c *QueryCache) SetHeightHint(h int64) {
	if h <= 0 {
		return
	}
	for {
		cur := c.hint.Load()
		if h <= cur {
			return
		}
		if c.hint.CompareAndSwap(cur, h) {
			return
		}
	}
}

func (c *QueryCache) HeightHint() int64 { return c.hint.Load() }

func (c *QueryCache) SnapshotStats() QueryCacheStats {
	c.mu.Lock()
	heights := len(c.byHeight)
	entries := len(c.lruIndex)
	totalBytes := c.totalBytes
	maxEntries := c.maxEntries
	maxBytes := c.maxBytes
	c.mu.Unlock()

	return QueryCacheStats{
		HeightHint:                   c.hint.Load(),
		Heights:                      heights,
		Entries:                      entries,
		TotalBytes:                   totalBytes,
		MaxEntries:                   maxEntries,
		MaxBytes:                     maxBytes,
		RequestsTotal:                c.stats.requestsTotal.Load(),
		CacheHitTotal:                c.stats.cacheHitTotal.Load(),
		CacheCorruptHitTotal:         c.stats.cacheCorruptHitTotal.Load(),
		CacheMissTotal:               c.stats.cacheMissTotal.Load(),
		BackendInvokeTotal:           c.stats.backendInvokeTotal.Load(),
		CacheWriteTotal:              c.stats.cacheWriteTotal.Load(),
		CacheWriteSkippedHeightTotal: c.stats.cacheWriteSkippedHeightTotal.Load(),
		CacheEvictTotal:              c.stats.cacheEvictTotal.Load(),
		InvokeErrorTotal:             c.stats.invokeErrorTotal.Load(),
	}
}

func (c *QueryCache) ResetStats() {
	c.stats.requestsTotal.Store(0)
	c.stats.cacheHitTotal.Store(0)
	c.stats.cacheCorruptHitTotal.Store(0)
	c.stats.cacheMissTotal.Store(0)
	c.stats.backendInvokeTotal.Store(0)
	c.stats.cacheWriteTotal.Store(0)
	c.stats.cacheWriteSkippedHeightTotal.Store(0)
	c.stats.cacheEvictTotal.Store(0)
	c.stats.invokeErrorTotal.Store(0)
}

func (c *QueryCache) lookup(height int64, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bucket, ok := c.byHeight[height]
	if !ok {
		return nil, false
	}
	v, hit := bucket[key]
	if !hit {
		return nil, false
	}
	if el, ok := c.lruIndex[lruKey{height: height, key: key}]; ok {
		c.lru.MoveToFront(el)
	}
	return v, true
}

func (c *QueryCache) store(height int64, key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	lk := lruKey{height: height, key: key}
	if el, ok := c.lruIndex[lk]; ok {
		ent := el.Value.(*lruEntry)
		c.totalBytes += int64(len(data)) - int64(ent.size)
		ent.size = len(data)
		c.lru.MoveToFront(el)
	} else {
		ent := &lruEntry{height: height, key: key, size: len(data)}
		c.lruIndex[lk] = c.lru.PushFront(ent)
		c.totalBytes += int64(ent.size)
	}

	bucket, ok := c.byHeight[height]
	if !ok {
		bucket = make(map[string][]byte)
		c.byHeight[height] = bucket
	}
	bucket[key] = data

	c.pruneLocked()
	c.evictLocked()
}

func (c *QueryCache) deleteEntry(height int64, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeEntryLocked(height, key)
}

func (c *QueryCache) removeEntryLocked(height int64, key string) {
	lk := lruKey{height: height, key: key}
	if el, ok := c.lruIndex[lk]; ok {
		c.totalBytes -= int64(el.Value.(*lruEntry).size)
		c.lru.Remove(el)
		delete(c.lruIndex, lk)
	}
	if bucket, ok := c.byHeight[height]; ok {
		delete(bucket, key)
		if len(bucket) == 0 {
			delete(c.byHeight, height)
		}
	}
}

func (c *QueryCache) pruneLocked() {
	if c.keepLast <= 0 {
		return
	}
	for len(c.byHeight) > c.keepLast {
		var oldest int64
		first := true
		for h := range c.byHeight {
			if first || h < oldest {
				oldest = h
				first = false
			}
		}
		c.dropHeightLocked(oldest)
	}
}

func (c *QueryCache) dropHeightLocked(height int64) {
	bucket, ok := c.byHeight[height]
	if !ok {
		return
	}
	for key := range bucket {
		lk := lruKey{height: height, key: key}
		if el, ok := c.lruIndex[lk]; ok {
			c.totalBytes -= int64(el.Value.(*lruEntry).size)
			c.lru.Remove(el)
			delete(c.lruIndex, lk)
		}
	}
	delete(c.byHeight, height)
}

func (c *QueryCache) evictLocked() {
	for c.overLimitLocked() {
		el := c.lru.Back()
		if el == nil {
			return
		}
		ent := el.Value.(*lruEntry)
		c.removeEntryLocked(ent.height, ent.key)
		c.stats.cacheEvictTotal.Add(1)
	}
}

func (c *QueryCache) overLimitLocked() bool {
	if c.maxEntries > 0 && len(c.lruIndex) > c.maxEntries {
		return true
	}
	if c.maxBytes > 0 && c.totalBytes > c.maxBytes {
		return true
	}
	return false
}

type CachingConn struct {
	inner grpc.ClientConnInterface
	cache *QueryCache
}

func (c *CachingConn) Invoke(ctx context.Context, method string, args, reply interface{}, opts ...grpc.CallOption) error {
	c.cache.stats.requestsTotal.Add(1)

	if shouldBypassQueryCache(ctx) {
		return c.invokeBackend(ctx, method, args, reply, opts...)
	}

	if !isSupportedProtoMessage(args) || !isSupportedProtoMessage(reply) {
		return c.invokeBackend(ctx, method, args, reply, opts...)
	}

	explicitHeight := heightFromOutgoingCtx(ctx)
	height := explicitHeight
	if height == 0 {
		height = c.cache.HeightHint()
	}
	if height == 0 {
		return c.invokeBackend(ctx, method, args, reply, opts...)
	}

	requestHash, hashErr := buildRequestHash(args)
	if hashErr != nil {
		return c.invokeBackend(ctx, method, args, reply, opts...)
	}
	key := buildCacheKey(method, requestHash)

	if cached, hit := c.cache.lookup(height, key); hit {
		if err := unmarshalProtoMessage(cached, reply); err == nil {
			c.cache.stats.cacheHitTotal.Add(1)
			if header := headerAddrFromCallOptions(opts); header != nil {
				*header = metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))
			}
			return nil
		}
		c.cache.stats.cacheCorruptHitTotal.Add(1)
		c.cache.deleteEntry(height, key)
	}
	c.cache.stats.cacheMissTotal.Add(1)

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

	sfKey := strconv.FormatInt(height, 10) + "|" + key
	loadOrFetch := func() (interface{}, error) {
		if cached, hit := c.cache.lookup(height, key); hit {
			return cacheInvokeResult{data: cached, height: height, fromCache: true, dataValid: true}, nil
		}

		if err := c.invokeBackend(pinnedCtx, method, args, reply, callOpts...); err != nil {
			return nil, err
		}

		responseHeight := heightFromIncomingMD(*responseHeader)
		if responseHeight == 0 {
			c.cache.stats.cacheWriteSkippedHeightTotal.Add(1)
			data, marshalErr := marshalProtoMessage(reply)
			if marshalErr != nil {
				return cacheInvokeResult{height: 0, leaderReply: reply}, nil
			}
			return cacheInvokeResult{data: data, height: 0, dataValid: true, leaderReply: reply}, nil
		}

		data, marshalErr := marshalProtoMessage(reply)
		if marshalErr != nil {
			return cacheInvokeResult{height: responseHeight, leaderReply: reply}, nil
		}

		c.cache.store(responseHeight, key, data)
		c.cache.stats.cacheWriteTotal.Add(1)
		c.cache.SetHeightHint(responseHeight)

		return cacheInvokeResult{data: data, height: responseHeight, dataValid: true, leaderReply: reply}, nil
	}

	resultAny, err, shared := c.cache.sfGroup.Do(sfKey, loadOrFetch)
	if err != nil {
		if shared && ctx.Err() == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			resultAny, err, shared = c.cache.sfGroup.Do(sfKey, loadOrFetch)
		}
		if err != nil {
			return err
		}
	}

	result, ok := resultAny.(cacheInvokeResult)
	if !ok {
		return fmt.Errorf("unexpected cache invoke result type: %T", resultAny)
	}

	if header := headerAddrFromCallOptions(opts); header != nil && result.height > 0 {
		*header = metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(result.height, 10))
	}

	if result.leaderReply == reply {
		return nil
	}

	if !result.dataValid {
		if shared {
			return c.invokeBackend(pinnedCtx, method, args, reply, callOpts...)
		}
		return nil
	}

	if err := unmarshalProtoMessage(result.data, reply); err != nil {
		if result.fromCache {
			c.cache.stats.cacheCorruptHitTotal.Add(1)
		}
		if result.height > 0 {
			c.cache.deleteEntry(result.height, key)
		}
		return c.invokeBackend(pinnedCtx, method, args, reply, callOpts...)
	}
	return nil
}

func (c *CachingConn) invokeBackend(ctx context.Context, method string, args, reply interface{}, opts ...grpc.CallOption) error {
	c.cache.stats.backendInvokeTotal.Add(1)
	if err := c.inner.Invoke(ctx, method, args, reply, opts...); err != nil {
		c.cache.stats.invokeErrorTotal.Add(1)
		return err
	}
	return nil
}

func (c *CachingConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return c.inner.NewStream(ctx, desc, method, opts...)
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
	h, _ := strconv.ParseInt(vals[len(vals)-1], 10, 64)
	return h
}

func setPinnedHeight(ctx context.Context, height int64) context.Context {
	value := strconv.FormatInt(height, 10)

	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return metadata.NewOutgoingContext(ctx, metadata.Pairs(grpctypes.GRPCBlockHeightHeader, value))
	}

	current := md.Get(grpctypes.GRPCBlockHeightHeader)
	if len(current) == 1 && current[0] == value {
		return ctx
	}

	md = md.Copy()
	md.Set(grpctypes.GRPCBlockHeightHeader, value)
	return metadata.NewOutgoingContext(ctx, md)
}

func PinHeight(ctx context.Context, height int64) context.Context {
	if height <= 0 {
		return ctx
	}
	return setPinnedHeight(ctx, height)
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

type cacheInvokeResult struct {
	data        []byte
	height      int64
	fromCache   bool
	dataValid   bool
	leaderReply interface{}
}

func buildRequestHash(msg interface{}) (string, error) {
	data, err := marshalProtoMessage(msg)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func buildCacheKey(method, requestHash string) string {
	return method + "|" + requestHash
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
		return googleproto.MarshalOptions{Deterministic: true}.Marshal(m)
	case gogoproto.Message:
		// Some gogoproto-generated types don't support deterministic marshal.
		buf := &gogoproto.Buffer{}
		buf.SetDeterministic(true)
		if err := buf.Marshal(m); err == nil {
			return buf.Bytes(), nil
		}
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
