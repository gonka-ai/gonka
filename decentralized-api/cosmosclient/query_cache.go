package cosmosclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/maypok86/otter/v2"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	googleproto "google.golang.org/protobuf/proto"
)

const (
	defaultMaxEntries = 200000
	defaultMaxBytes   = 1 << 30 // 1 GiB
	defaultMaxHintAge = 30 * time.Second
	defaultEntryTTL   = 30 * time.Second
	entryOverhead     = 128
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
	CachePruneTotal              uint64 `json:"cache_prune_total"`
	StaleHintBypassTotal         uint64 `json:"stale_hint_bypass_total"`
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
	cachePruneTotal              atomic.Uint64
	staleHintBypassTotal         atomic.Uint64
	invokeErrorTotal             atomic.Uint64
}

type QueryCache struct {
	hint      atomic.Int64
	hintSetAt atomic.Int64

	maxHintAge time.Duration

	entries    *otter.Cache[string, []byte]
	maxEntries int
	maxBytes   int64

	sfGroup singleflight.Group
	stats   queryCacheCounters
}

type queryCacheConfig struct {
	maxEntries int
	maxBytes   int64
	entryTTL   time.Duration
	clock      otter.Clock
	executor   func(func())
}

func NewQueryCache() *QueryCache {
	return NewQueryCacheWithLimits(defaultMaxEntries, defaultMaxBytes)
}

func NewQueryCacheWithLimits(maxEntries int, maxBytes int64) *QueryCache {
	return newQueryCache(queryCacheConfig{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		entryTTL:   defaultEntryTTL,
	})
}

func newQueryCache(cfg queryCacheConfig) *QueryCache {
	c := &QueryCache{
		maxHintAge: defaultMaxHintAge,
		maxEntries: cfg.maxEntries,
		maxBytes:   cfg.maxBytes,
	}

	opts := &otter.Options[string, []byte]{
		ExpiryCalculator: otter.ExpiryCreating[string, []byte](cfg.entryTTL),
		OnDeletion: func(e otter.DeletionEvent[string, []byte]) {
			switch e.Cause {
			case otter.CauseOverflow:
				c.stats.cacheEvictTotal.Add(1)
			case otter.CauseExpiration:
				c.stats.cachePruneTotal.Add(1)
			}
		},
		Executor: cfg.executor,
		Clock:    cfg.clock,
	}
	switch {
	case cfg.maxBytes > 0:
		opts.MaximumWeight = uint64(cfg.maxBytes)
		opts.Weigher = newEntryWeigher(cfg.maxEntries, cfg.maxBytes)
	case cfg.maxEntries > 0:
		opts.MaximumSize = cfg.maxEntries
	}

	c.entries = otter.Must(opts)
	return c
}

func newEntryWeigher(maxEntries int, maxBytes int64) func(key string, value []byte) uint32 {
	var minWeight uint64
	if maxEntries > 0 {
		minWeight = (uint64(maxBytes) + uint64(maxEntries) - 1) / uint64(maxEntries)
	}
	return func(key string, value []byte) uint32 {
		weight := uint64(len(key)+len(value)) + entryOverhead
		if weight < minWeight {
			weight = minWeight
		}
		if weight > math.MaxUint32 {
			return math.MaxUint32
		}
		return uint32(weight)
	}
}

func entryKey(height int64, key string) string {
	return strconv.FormatInt(height, 10) + "|" + key
}

func (c *QueryCache) SetHeightHint(h int64) {
	if h <= 0 {
		return
	}
	for {
		cur := c.hint.Load()
		if h < cur {
			return
		}
		if h == cur {
			c.hintSetAt.Store(time.Now().UnixNano())
			return
		}
		if c.hint.CompareAndSwap(cur, h) {
			c.hintSetAt.Store(time.Now().UnixNano())
			return
		}
	}
}

func (c *QueryCache) hintFresh() bool {
	if c.maxHintAge <= 0 {
		return true
	}
	setAt := c.hintSetAt.Load()
	if setAt == 0 {
		return false
	}
	return time.Since(time.Unix(0, setAt)) <= c.maxHintAge
}

func (c *QueryCache) HeightHint() int64 { return c.hint.Load() }

func (c *QueryCache) SnapshotStats() QueryCacheStats {
	return QueryCacheStats{
		HeightHint:                   c.hint.Load(),
		Entries:                      c.entries.EstimatedSize(),
		TotalBytes:                   int64(c.entries.WeightedSize()),
		MaxEntries:                   c.maxEntries,
		MaxBytes:                     c.maxBytes,
		RequestsTotal:                c.stats.requestsTotal.Load(),
		CacheHitTotal:                c.stats.cacheHitTotal.Load(),
		CacheCorruptHitTotal:         c.stats.cacheCorruptHitTotal.Load(),
		CacheMissTotal:               c.stats.cacheMissTotal.Load(),
		BackendInvokeTotal:           c.stats.backendInvokeTotal.Load(),
		CacheWriteTotal:              c.stats.cacheWriteTotal.Load(),
		CacheWriteSkippedHeightTotal: c.stats.cacheWriteSkippedHeightTotal.Load(),
		CacheEvictTotal:              c.stats.cacheEvictTotal.Load(),
		CachePruneTotal:              c.stats.cachePruneTotal.Load(),
		StaleHintBypassTotal:         c.stats.staleHintBypassTotal.Load(),
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
	c.stats.cachePruneTotal.Store(0)
	c.stats.staleHintBypassTotal.Store(0)
	c.stats.invokeErrorTotal.Store(0)
}

func (c *QueryCache) lookup(height int64, key string) ([]byte, bool) {
	return c.entries.GetIfPresent(entryKey(height, key))
}

func (c *QueryCache) store(height int64, key string, data []byte) {
	c.entries.Set(entryKey(height, key), data)
}

func (c *QueryCache) deleteEntry(height int64, key string) {
	c.entries.Invalidate(entryKey(height, key))
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
	hintTrusted := true
	if height == 0 {
		hintTrusted = c.cache.hintFresh()
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

	if hintTrusted {
		if cached, hit := c.cache.lookup(height, key); hit {
			if err := unmarshalProtoMessage(cached, reply); err == nil {
				c.cache.stats.cacheHitTotal.Add(1)
				mergeHeightIntoHeader(headerAddrFromCallOptions(opts), height)
				return nil
			}
			c.cache.stats.cacheCorruptHitTotal.Add(1)
			c.cache.deleteEntry(height, key)
		}
	} else {
		c.cache.stats.staleHintBypassTotal.Add(1)
	}
	c.cache.stats.cacheMissTotal.Add(1)

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
		if hintTrusted {
			if cached, hit := c.cache.lookup(height, key); hit {
				return cacheInvokeResult{data: cached, height: height, fromCache: true, dataValid: true}, nil
			}
		}

		if err := c.invokeBackend(ctx, method, args, reply, callOpts...); err != nil {
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

	mergeHeightIntoHeader(headerAddrFromCallOptions(opts), result.height)

	if result.leaderReply == reply {
		return nil
	}

	if !result.dataValid {
		if shared {
			return c.invokeBackend(ctx, method, args, reply, callOpts...)
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
		return c.invokeBackend(ctx, method, args, reply, callOpts...)
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

func mergeHeightIntoHeader(header *metadata.MD, height int64) {
	if header == nil || height <= 0 {
		return
	}
	md := *header
	if md == nil {
		md = metadata.MD{}
	} else {
		md = md.Copy()
	}
	md.Set(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))
	*header = md
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
