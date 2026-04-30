package keeper

import (
	"context"
	"os"
	"sync"
	"unsafe"

	"cosmossdk.io/collections"
	collcodec "cosmossdk.io/collections/codec"
	corestore "cosmossdk.io/core/store"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
)

var occConflictDetectionEnabled bool

func init() {
	occConflictDetectionEnabled = os.Getenv("COSMOS_OCC_ENABLED") == "1"
}

// IsOCCConflictDetectionEnabled reports whether COSMOS_OCC_ENABLED=1 is set.
// When enabled, read/write sets are tracked per transaction so that
// conflicts can be detected post-execution via DetectConflicts.
func IsOCCConflictDetectionEnabled() bool {
	return occConflictDetectionEnabled
}

// StoreBackend plugs an OptimisticStore into any persistent storage.
type StoreBackend[K comparable, V any] struct {
	Load   func(ctx context.Context, key K) (V, bool)
	Save   func(ctx context.Context, key K, val V)
	Delete func(ctx context.Context, key K)
	Clone  func(val V) V
}

// OptimisticStoreConfig controls which cache layers are active.
type OptimisticStoreConfig struct {
	BlockCacheEnabled bool
	TxDraftEnabled    bool
}

// OptimisticStore is a generic, OCC-aware cache wrapper for any keyed store.
// K must be comparable (used as map key). V is the value type.
//
// Layers (checked in order on Get):
//  1. Tx draft (per-tx, context-scoped, write-behind)
//  2. Block cache (per-block, in-memory, flushed to store in EndBlock)
//  3. Store backend (persistent)
type OptimisticStore[K comparable, V any] struct {
	mu        sync.RWMutex
	m         map[K]V
	dirty     bool
	backend   StoreBackend[K, V]
	config    OptimisticStoreConfig
	conflicts conflictTracker[K]

	ctxKeyDraft       any
	ctxKeyBranchDraft any
}

// NewOptimisticStore creates an OptimisticStore with the given backend and config.
// ctxKeyDraft and ctxKeyBranchDraft must be unique context key types per store instance.
func NewOptimisticStore[K comparable, V any](
	backend StoreBackend[K, V],
	config OptimisticStoreConfig,
	ctxKeyDraft any,
	ctxKeyBranchDraft any,
) *OptimisticStore[K, V] {
	return &OptimisticStore[K, V]{
		m:                 make(map[K]V),
		backend:           backend,
		config:            config,
		ctxKeyDraft:       ctxKeyDraft,
		ctxKeyBranchDraft: ctxKeyBranchDraft,
	}
}

// ---------- conflict tracker ----------

type conflictTracker[K comparable] struct {
	mu        sync.Mutex
	readSets  map[uintptr]map[K]struct{}
	writeSets map[uintptr]map[K]struct{}
}

func (t *conflictTracker[K]) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.readSets = make(map[uintptr]map[K]struct{})
	t.writeSets = make(map[uintptr]map[K]struct{})
}

func (t *conflictTracker[K]) registerTx(txID uintptr) {
	if txID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.readSets == nil {
		t.readSets = make(map[uintptr]map[K]struct{})
		t.writeSets = make(map[uintptr]map[K]struct{})
	}
	if _, ok := t.readSets[txID]; !ok {
		t.readSets[txID] = make(map[K]struct{})
	}
	if _, ok := t.writeSets[txID]; !ok {
		t.writeSets[txID] = make(map[K]struct{})
	}
}

func (t *conflictTracker[K]) recordRead(txID uintptr, key K) {
	if !occConflictDetectionEnabled || txID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if rs, ok := t.readSets[txID]; ok {
		rs[key] = struct{}{}
	}
}

func (t *conflictTracker[K]) recordWrite(txID uintptr, key K) {
	if !occConflictDetectionEnabled || txID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if ws, ok := t.writeSets[txID]; ok {
		ws[key] = struct{}{}
	}
}

func (t *conflictTracker[K]) unregisterTx(txID uintptr) {
	if txID == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.readSets, txID)
	delete(t.writeSets, txID)
}

func (t *conflictTracker[K]) detectConflicts() (conflictedReads, conflictedWrites []uintptr) {
	t.mu.Lock()
	defer t.mu.Unlock()
	seenR := make(map[uintptr]struct{})
	seenW := make(map[uintptr]struct{})

	for readerTx, readKeys := range t.readSets {
		for writerTx, writeKeys := range t.writeSets {
			if readerTx == writerTx {
				continue
			}
			for key := range readKeys {
				if _, written := writeKeys[key]; written {
					if _, already := seenR[readerTx]; !already {
						seenR[readerTx] = struct{}{}
						conflictedReads = append(conflictedReads, readerTx)
					}
					if _, already := seenW[writerTx]; !already {
						seenW[writerTx] = struct{}{}
						conflictedWrites = append(conflictedWrites, writerTx)
					}
					break
				}
			}
		}
	}

	txIDs := make([]uintptr, 0, len(t.writeSets))
	for txID := range t.writeSets {
		txIDs = append(txIDs, txID)
	}
	for i := 0; i < len(txIDs); i++ {
		for j := i + 1; j < len(txIDs); j++ {
			a, b := txIDs[i], txIDs[j]
			for key := range t.writeSets[a] {
				if _, ok := t.writeSets[b][key]; ok {
					if _, already := seenW[a]; !already {
						seenW[a] = struct{}{}
						conflictedWrites = append(conflictedWrites, a)
					}
					if _, already := seenW[b]; !already {
						seenW[b] = struct{}{}
						conflictedWrites = append(conflictedWrites, b)
					}
					break
				}
			}
		}
	}
	return
}

// ---------- draft (tx-scoped write-behind) ----------

type storeDraft[K comparable, V any] struct {
	m       map[K]V
	removed map[K]struct{}
}

type lazyStoreDraft[K comparable, V any] struct {
	once sync.Once
	d    *storeDraft[K, V]
}

func (l *lazyStoreDraft[K, V]) get() *storeDraft[K, V] {
	return l.d
}

func (l *lazyStoreDraft[K, V]) getOrCreate() *storeDraft[K, V] {
	l.once.Do(func() {
		l.d = &storeDraft[K, V]{
			m:       make(map[K]V),
			removed: make(map[K]struct{}),
		}
	})
	return l.d
}

// ---------- context helpers ----------

func (s *OptimisticStore[K, V]) txID(ctx context.Context) uintptr {
	v := ctx.Value(s.ctxKeyDraft)
	if v == nil {
		return 0
	}
	if p, ok := v.(*lazyStoreDraft[K, V]); ok {
		return uintptr(unsafe.Pointer(p))
	}
	return 0
}

// WithDraft attaches a lazy tx-scoped draft to ctx. Call at tx start (AnteHandler).
func (s *OptimisticStore[K, V]) WithDraft(ctx context.Context) context.Context {
	if !s.config.TxDraftEnabled {
		return ctx
	}
	return context.WithValue(ctx, s.ctxKeyDraft, &lazyStoreDraft[K, V]{})
}

func (s *OptimisticStore[K, V]) getDraft(ctx context.Context) *storeDraft[K, V] {
	if ctx == nil {
		return nil
	}
	if d := s.getBranchDraft(ctx); d != nil {
		return d
	}
	v := ctx.Value(s.ctxKeyDraft)
	if v == nil {
		return nil
	}
	if h, ok := v.(*lazyStoreDraft[K, V]); ok {
		return h.get()
	}
	return nil
}

func (s *OptimisticStore[K, V]) getBranchDraft(ctx context.Context) *storeDraft[K, V] {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(s.ctxKeyBranchDraft)
	if v == nil {
		return nil
	}
	if h, ok := v.(*lazyStoreDraft[K, V]); ok {
		return h.get()
	}
	return nil
}

func (s *OptimisticStore[K, V]) getDraftForWrite(ctx context.Context) *storeDraft[K, V] {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(s.ctxKeyBranchDraft); v != nil {
		if h, ok := v.(*lazyStoreDraft[K, V]); ok {
			return h.getOrCreate()
		}
	}
	v := ctx.Value(s.ctxKeyDraft)
	if v == nil {
		return nil
	}
	if h, ok := v.(*lazyStoreDraft[K, V]); ok {
		return h.getOrCreate()
	}
	return nil
}

// WithBranchDraft returns a new context with a branch draft layered on top.
func (s *OptimisticStore[K, V]) WithBranchDraft(ctx context.Context) context.Context {
	if !s.config.TxDraftEnabled {
		return ctx
	}
	return context.WithValue(ctx, s.ctxKeyBranchDraft, &lazyStoreDraft[K, V]{})
}

// MergeBranch merges a branch draft into the parent draft.
func (s *OptimisticStore[K, V]) MergeBranch(parentCtx, branchCtx context.Context) {
	branch := s.getBranchDraft(branchCtx)
	if branch == nil {
		return
	}
	parent := s.getDraftForWrite(parentCtx)
	if parent == nil {
		return
	}
	for key, val := range branch.m {
		parent.m[key] = s.backend.Clone(val)
	}
	for key := range branch.removed {
		parent.removed[key] = struct{}{}
		delete(parent.m, key)
	}
}

// ---------- public CRUD ----------

// Get reads the value for key. Layer order: draft → block cache → store backend.
func (s *OptimisticStore[K, V]) Get(ctx context.Context, key K) (val V, found bool) {
	if s.config.TxDraftEnabled {
		if draft := s.getDraft(ctx); draft != nil {
			if _, removed := draft.removed[key]; removed {
				var zero V
				return zero, false
			}
			if cached, ok := draft.m[key]; ok {
				return s.backend.Clone(cached), true
			}
		}
	}

	if s.config.BlockCacheEnabled {
		s.mu.RLock()
		if cached, ok := s.m[key]; ok {
			s.mu.RUnlock()
			s.conflicts.recordRead(s.txID(ctx), key)
			return s.backend.Clone(cached), true
		}
		s.mu.RUnlock()
	}

	val, found = s.backend.Load(ctx, key)
	if !found {
		var zero V
		return zero, false
	}
	if s.config.BlockCacheEnabled {
		s.mu.Lock()
		s.m[key] = s.backend.Clone(val)
		s.mu.Unlock()
	}
	s.conflicts.recordRead(s.txID(ctx), key)
	return val, true
}

// Set writes a value. Goes to draft if available, otherwise to block cache or store.
func (s *OptimisticStore[K, V]) Set(ctx context.Context, key K, val V) {
	if s.config.TxDraftEnabled {
		if draft := s.getDraftForWrite(ctx); draft != nil {
			s.conflicts.recordWrite(s.txID(ctx), key)
			draft.m[key] = s.backend.Clone(val)
			delete(draft.removed, key)
			return
		}
	}

	if s.config.BlockCacheEnabled {
		s.mu.Lock()
		s.m[key] = s.backend.Clone(val)
		s.dirty = true
		s.mu.Unlock()
		return
	}

	s.backend.Save(ctx, key, val)
}

// Remove deletes a value. Goes to draft tombstone if available, otherwise to block cache/store.
func (s *OptimisticStore[K, V]) Remove(ctx context.Context, key K) {
	if s.config.TxDraftEnabled {
		if draft := s.getDraftForWrite(ctx); draft != nil {
			s.conflicts.recordWrite(s.txID(ctx), key)
			draft.removed[key] = struct{}{}
			delete(draft.m, key)
			return
		}
	}

	if s.config.BlockCacheEnabled {
		s.mu.Lock()
		delete(s.m, key)
		s.dirty = true
		s.mu.Unlock()
	}

	s.backend.Delete(ctx, key)
}

// ---------- lifecycle ----------

// RegisterTx registers a tx for OCC tracking.
func (s *OptimisticStore[K, V]) RegisterTx(ctx context.Context) {
	s.conflicts.registerTx(s.txID(ctx))
}

// CommitDraft merges the tx draft into the block cache. Call from PostHandler on success.
func (s *OptimisticStore[K, V]) CommitDraft(ctx context.Context) {
	txID := s.txID(ctx)
	draft := s.getDraft(ctx)
	if draft == nil {
		s.conflicts.unregisterTx(txID)
		return
	}

	if s.config.BlockCacheEnabled {
		s.mu.Lock()
		for key, val := range draft.m {
			s.m[key] = s.backend.Clone(val)
			s.dirty = true
		}
		for key := range draft.removed {
			delete(s.m, key)
			s.dirty = true
		}
		s.mu.Unlock()
	} else {
		for key, val := range draft.m {
			s.backend.Save(ctx, key, val)
		}
		for key := range draft.removed {
			s.backend.Delete(ctx, key)
		}
	}
	s.conflicts.unregisterTx(txID)
}

// ReleaseDraft discards the tx draft. Call from PostHandler on failure.
func (s *OptimisticStore[K, V]) ReleaseDraft(ctx context.Context) {
	s.conflicts.unregisterTx(s.txID(ctx))
}

// Invalidate clears the block cache and conflict tracker. Call from BeginBlock.
func (s *OptimisticStore[K, V]) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = make(map[K]V)
	s.dirty = false
	s.conflicts.reset()
}

// BlockCacheValues returns cloned copies of all values currently in the block cache.
// Useful for queries that need to see uncommitted-to-store data without flushing.
func (s *OptimisticStore[K, V]) BlockCacheValues() []V {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vals := make([]V, 0, len(s.m))
	for _, v := range s.m {
		vals = append(vals, s.backend.Clone(v))
	}
	return vals
}

// Flush persists all dirty block-cache entries to the store backend. Call from EndBlock.
func (s *OptimisticStore[K, V]) Flush(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return
	}
	for key, val := range s.m {
		s.backend.Save(ctx, key, val)
	}
	s.dirty = false
}

// DetectConflicts returns txIDs involved in read-write or write-write collisions.
func (s *OptimisticStore[K, V]) DetectConflicts() (conflictedReads, conflictedWrites []uintptr) {
	return s.conflicts.detectConflicts()
}

// ResetConflictTracker clears all tracked read/write sets.
func (s *OptimisticStore[K, V]) ResetConflictTracker() {
	s.conflicts.reset()
}

// ---------- OptimisticItem (singleton sugar) ----------

type singletonKey = struct{}

// OptimisticItem is a singleton-keyed OptimisticStore. Use for stores like Params
// that hold a single value rather than a map.
type OptimisticItem[V any] struct {
	inner *OptimisticStore[singletonKey, V]
}

func NewOptimisticItem[V any](
	backend StoreBackend[singletonKey, V],
	config OptimisticStoreConfig,
	ctxKeyDraft any,
	ctxKeyBranchDraft any,
) *OptimisticItem[V] {
	return &OptimisticItem[V]{
		inner: NewOptimisticStore[singletonKey, V](backend, config, ctxKeyDraft, ctxKeyBranchDraft),
	}
}

func (item *OptimisticItem[V]) Get(ctx context.Context) (V, bool) {
	return item.inner.Get(ctx, singletonKey{})
}

func (item *OptimisticItem[V]) Set(ctx context.Context, val V) {
	item.inner.Set(ctx, singletonKey{}, val)
}

func (item *OptimisticItem[V]) Store() *OptimisticStore[singletonKey, V] {
	return item.inner
}

// ---------- OptimisticStoreGroup (batch lifecycle) ----------

// optimisticStoreOps is the non-generic interface satisfied by every OptimisticStore,
// enabling batch operations across stores with different key/value types.
type optimisticStoreOps interface {
	WithDraft(ctx context.Context) context.Context
	RegisterTx(ctx context.Context)
	CommitDraft(ctx context.Context)
	ReleaseDraft(ctx context.Context)
	Invalidate()
	Flush(ctx context.Context)
	WithBranchDraft(ctx context.Context) context.Context
	MergeBranch(parentCtx, branchCtx context.Context)
}

// OptimisticStoreGroup collects all optimistic stores and provides batch lifecycle methods.
type OptimisticStoreGroup struct {
	stores []optimisticStoreOps
}

// Register adds a store to the group. Call during keeper init.
func (g *OptimisticStoreGroup) Register(s optimisticStoreOps) {
	g.stores = append(g.stores, s)
}

// WithDraftAll attaches tx-scoped drafts for every registered store. Call from AnteHandler.
func (g *OptimisticStoreGroup) WithDraftAll(ctx context.Context) context.Context {
	for _, s := range g.stores {
		ctx = s.WithDraft(ctx)
	}
	return ctx
}

// RegisterTxAll registers the current tx for OCC tracking in every store.
func (g *OptimisticStoreGroup) RegisterTxAll(ctx context.Context) {
	for _, s := range g.stores {
		s.RegisterTx(ctx)
	}
}

// CommitDraftAll commits all tx drafts into the block caches. Call from PostHandler on success.
func (g *OptimisticStoreGroup) CommitDraftAll(ctx context.Context) {
	for _, s := range g.stores {
		s.CommitDraft(ctx)
	}
}

// ReleaseDraftAll discards all tx drafts. Call from PostHandler on failure.
func (g *OptimisticStoreGroup) ReleaseDraftAll(ctx context.Context) {
	for _, s := range g.stores {
		s.ReleaseDraft(ctx)
	}
}

// InvalidateAll clears all block caches and conflict trackers. Call from BeginBlock.
func (g *OptimisticStoreGroup) InvalidateAll() {
	for _, s := range g.stores {
		s.Invalidate()
	}
}

// FlushAll persists all dirty block caches to their backends. Call from EndBlock.
func (g *OptimisticStoreGroup) FlushAll(ctx context.Context) {
	for _, s := range g.stores {
		s.Flush(ctx)
	}
}

// CacheContext returns a cached SDK context with branch drafts for all registered stores.
// Call writeCache() on success to merge both the SDK store cache and all branch drafts.
func (g *OptimisticStoreGroup) CacheContext(ctx sdk.Context) (sdk.Context, func()) {
	cacheCtx, writeSDKCache := ctx.CacheContext()
	goCtx := cacheCtx.Context()
	for _, s := range g.stores {
		goCtx = s.WithBranchDraft(goCtx)
	}
	cacheCtx = cacheCtx.WithContext(goCtx)

	var written bool
	return cacheCtx, func() {
		if written {
			return
		}
		written = true
		for _, s := range g.stores {
			s.MergeBranch(ctx.Context(), cacheCtx.Context())
		}
		writeSDKCache()
	}
}

// ---------- context key auto-generation ----------

// storeCtxKey is used as a context key for drafts, auto-generated from the store name.
type storeCtxKey struct {
	name   string
	branch bool
}

// ---------- smart constructors ----------

// OptimisticCollMap bundles a collections.Map with an OptimisticStore that caches it.
// CK is the comparable cache key, CollK is the collections key, V is the value (must be a proto.Message).
type OptimisticCollMap[CK comparable, CollK, V any] struct {
	*OptimisticStore[CK, V]
	Map collections.Map[CollK, V]
}

// NewOptimisticCollMap creates a collections.Map AND an OptimisticStore wrapping it in one call.
// toCollKey converts the cache key to the collections key for Load/Save/Delete.
//
// Usage (same params as collections.NewMap, plus config and key converter):
//
//	store := NewOptimisticCollMap(sb, prefix, name, keyCodec, valueCodec, config,
//	    func(k MyCacheKey) collections.Pair[uint64, string] { return collections.Join(k.Epoch, k.ModelId) })
func NewOptimisticCollMap[CK comparable, CollK, V any, PV interface {
	*V
	proto.Message
}](
	sb *collections.SchemaBuilder,
	prefix collections.Prefix,
	name string,
	keyCodec collcodec.KeyCodec[CollK],
	valueCodec collcodec.ValueCodec[V],
	config OptimisticStoreConfig,
	toCollKey func(CK) CollK,
) *OptimisticCollMap[CK, CollK, V] {
	collMap := collections.NewMap(sb, prefix, name, keyCodec, valueCodec)
	backend := StoreBackend[CK, V]{
		Load: func(ctx context.Context, key CK) (V, bool) {
			val, err := collMap.Get(ctx, toCollKey(key))
			if err != nil {
				var zero V
				return zero, false
			}
			return val, true
		},
		Save: func(ctx context.Context, key CK, val V) {
			_ = collMap.Set(ctx, toCollKey(key), val)
		},
		Delete: func(ctx context.Context, key CK) {
			_ = collMap.Remove(ctx, toCollKey(key))
		},
		Clone: func(val V) V {
			pv := PV(&val)
			cloned := proto.Clone(pv).(PV)
			return *cloned
		},
	}
	return &OptimisticCollMap[CK, CollK, V]{
		OptimisticStore: NewOptimisticStore(backend, config,
			storeCtxKey{name: name, branch: false},
			storeCtxKey{name: name, branch: true},
		),
		Map: collMap,
	}
}

// NewOptimisticProtoItem creates an OptimisticItem for a single proto value stored at a raw KV key.
// Handles marshal/unmarshal and proto.Clone automatically.
//
// Usage:
//
//	paramsStore := NewOptimisticProtoItem[types.Params](storeService, cdc, types.ParamsKey, "params", config)
func NewOptimisticProtoItem[V any, PV interface {
	*V
	proto.Message
}](
	storeService corestore.KVStoreService,
	cdc codec.BinaryCodec,
	kvKey []byte,
	name string,
	config OptimisticStoreConfig,
) *OptimisticItem[V] {
	backend := StoreBackend[singletonKey, V]{
		Load: func(ctx context.Context, _ singletonKey) (V, bool) {
			store := runtime.KVStoreAdapter(storeService.OpenKVStore(ctx))
			bz := store.Get(kvKey)
			if bz == nil {
				var zero V
				return zero, false
			}
			var val V
			if err := cdc.Unmarshal(bz, PV(&val)); err != nil {
				var zero V
				return zero, false
			}
			return val, true
		},
		Save: func(ctx context.Context, _ singletonKey, val V) {
			store := runtime.KVStoreAdapter(storeService.OpenKVStore(ctx))
			bz, err := cdc.Marshal(PV(&val))
			if err != nil {
				return
			}
			store.Set(kvKey, bz)
		},
		Delete: func(ctx context.Context, _ singletonKey) {
			store := runtime.KVStoreAdapter(storeService.OpenKVStore(ctx))
			store.Delete(kvKey)
		},
		Clone: func(val V) V {
			pv := PV(&val)
			cloned := proto.Clone(pv).(PV)
			return *cloned
		},
	}
	return NewOptimisticItem(backend, config,
		storeCtxKey{name: name, branch: false},
		storeCtxKey{name: name, branch: true},
	)
}
