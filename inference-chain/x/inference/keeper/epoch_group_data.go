package keeper

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"cosmossdk.io/collections"
	"github.com/cosmos/gogoproto/proto"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// cosmosOptimisticCachesEnabled is set once at package init from COSMOS_OPTIMISTIC_CACHES=1.
var cosmosOptimisticCachesEnabled bool

func init() {
	cosmosOptimisticCachesEnabled = os.Getenv("COSMOS_OPTIMISTIC_CACHES") == "1"
}

// getGID returns the current goroutine id (for reentrant lock holder identity). Uses runtime.Stack; for use in reentrant locking only.
func getGID() int64 {
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	buf = buf[:n]
	idx := bytes.Index(buf, []byte("goroutine "))
	if idx < 0 {
		return 0
	}
	buf = buf[idx+len("goroutine "):]
	idx = bytes.Index(buf, []byte(" "))
	if idx < 0 {
		return 0
	}
	gid, _ := strconv.ParseInt(string(buf[:idx]), 10, 64)
	return gid
}

// ctxKeyEpochGroupDraft is the context key for the tx-scoped EpochGroupData draft.
type ctxKeyEpochGroupDraft struct{}

// ctxKeyEpochGroupBranchDraft is the context key for a cache-branch-scoped EpochGroupData draft.
// When set (e.g. via CacheContextWithEpochGroupBranch), reads/writes use this draft instead of the main draft.
// The branch draft is merged into the parent draft when the branch's writeCache() is called.
type ctxKeyEpochGroupBranchDraft struct{}

// lazyEpochGroupDraft defers allocation of the real draft until the first write (Set/Remove).
// This avoids allocating a map for every tx when most txs only read epoch group data.
type lazyEpochGroupDraft struct {
	once sync.Once
	d    *epochGroupDraft
}

func (l *lazyEpochGroupDraft) get() *epochGroupDraft {
	return l.d
}

func (l *lazyEpochGroupDraft) getOrCreate() *epochGroupDraft {
	l.once.Do(func() {
		l.d = &epochGroupDraft{m: make(map[epochGroupCacheKey]types.EpochGroupData)}
	})
	return l.d
}

// epochGroupDraft holds tx-scoped EpochGroupData writes for optimistic parallel execution.
// Uses a reentrant write lock: the same goroutine can write (or delete) multiple times; lock is released only in PostHandler (commit or failure).
type epochGroupDraft struct {
	mu          sync.RWMutex
	m           map[epochGroupCacheKey]types.EpochGroupData
	writeHolder int64 // goroutine id of the writer (for reentrancy)
	writeCount  int32 // number of Lock() calls not yet Unlock()ed by the holder
}

// lockWrite acquires the write lock; reentrant for the same goroutine (increments count). Release via unlockWrite or releaseWriteLock.
// Only takes the lock when COSMOS_OPTIMISTIC_CACHES=1; otherwise no-op.
func (d *epochGroupDraft) lockWrite() {
	if !cosmosOptimisticCachesEnabled {
		return
	}
	gid := getGID()
	if atomic.LoadInt64(&d.writeHolder) == gid {
		atomic.AddInt32(&d.writeCount, 1)
		return
	}
	d.mu.Lock()
	atomic.StoreInt64(&d.writeHolder, gid)
	atomic.StoreInt32(&d.writeCount, 1)
}

// unlockWrite releases one level of the write lock; if count goes to 0, releases the underlying RWMutex.
// No-op when COSMOS_OPTIMISTIC_CACHES is not 1.
func (d *epochGroupDraft) unlockWrite() {
	if !cosmosOptimisticCachesEnabled {
		return
	}
	if atomic.AddInt32(&d.writeCount, -1) == 0 {
		atomic.StoreInt64(&d.writeHolder, 0)
		d.mu.Unlock()
	}
}

// isWriteLocked returns true if any goroutine holds the write lock (for readers: if true and we are the holder we can read without RLock).
func (d *epochGroupDraft) isWriteLocked() bool {
	return atomic.LoadInt32(&d.writeCount) > 0
}

// releaseWriteLock fully releases the write lock (for PostHandler: unlock until count is 0).
func (d *epochGroupDraft) releaseWriteLock() {
	for atomic.LoadInt32(&d.writeCount) > 0 {
		d.unlockWrite()
	}
}

// WithEpochGroupDraft attaches a lazy tx-scoped draft holder to ctx. Call at tx start (e.g. from AnteHandler).
// The real draft (and its map) is created only on first SetEpochGroupData or RemoveEpochGroupData for current/previous epoch;
// read-only txs never allocate it and always use the per-block cache (or store).
func WithEpochGroupDraft(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyEpochGroupDraft{}, &lazyEpochGroupDraft{})
}

// getEpochGroupDraftFromContext returns the draft from ctx if it exists, or nil. Does not create the draft (for reads and for Commit/Release).
// When ctx has a branch draft (e.g. from CacheContextWithEpochGroupBranch), that is preferred over the main tx draft.
func getEpochGroupDraftFromContext(ctx context.Context) *epochGroupDraft {
	if ctx == nil {
		return nil
	}
	if d := getEpochGroupBranchDraftFromContext(ctx); d != nil {
		return d
	}
	v := ctx.Value(ctxKeyEpochGroupDraft{})
	if v == nil {
		return nil
	}
	switch h := v.(type) {
	case *lazyEpochGroupDraft:
		return h.get()
	case *epochGroupDraft:
		return h
	default:
		return nil
	}
}

// getEpochGroupBranchDraftFromContext returns only the branch draft from ctx (ctxKeyEpochGroupBranchDraft), or nil.
// Does not create the draft. Used when merging a branch into the parent.
func getEpochGroupBranchDraftFromContext(ctx context.Context) *epochGroupDraft {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(ctxKeyEpochGroupBranchDraft{})
	if v == nil {
		return nil
	}
	switch h := v.(type) {
	case *lazyEpochGroupDraft:
		return h.get()
	case *epochGroupDraft:
		return h
	default:
		return nil
	}
}

// getEpochGroupDraftForWriteFromContext returns the draft, creating it from the lazy holder on first write. Use in SetEpochGroupData and RemoveEpochGroupData.
// When ctx has a branch draft (e.g. from CacheContextWithEpochGroupBranch), that is preferred over the main tx draft.
func getEpochGroupDraftForWriteFromContext(ctx context.Context) *epochGroupDraft {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(ctxKeyEpochGroupBranchDraft{}); v != nil {
		switch h := v.(type) {
		case *lazyEpochGroupDraft:
			return h.getOrCreate()
		case *epochGroupDraft:
			return h
		}
	}
	v := ctx.Value(ctxKeyEpochGroupDraft{})
	if v == nil {
		return nil
	}
	switch h := v.(type) {
	case *lazyEpochGroupDraft:
		return h.getOrCreate()
	case *epochGroupDraft:
		return h
	default:
		return nil
	}
}

// CommitEpochGroupDraftFromContext merges the draft from ctx into the block cache. Call from PostHandler on tx success.
// Does not write to store; FlushCurrentEpochGroupCache in EndBlock persists. Releases the draft write lock if held.
func (k Keeper) CommitEpochGroupDraftFromContext(ctx context.Context) {
	draft := getEpochGroupDraftFromContext(ctx)
	if draft == nil {
		return
	}
	var snapshot map[epochGroupCacheKey]types.EpochGroupData
	if draft.isWriteLocked() {
		// This tx holds the write lock (same goroutine as PostHandler); read directly then release.
		snapshot = make(map[epochGroupCacheKey]types.EpochGroupData, len(draft.m))
		for key, val := range draft.m {
			snapshot[key] = val
		}
		draft.releaseWriteLock()
	} else {
		draft.mu.RLock()
		snapshot = make(map[epochGroupCacheKey]types.EpochGroupData, len(draft.m))
		for key, val := range draft.m {
			snapshot[key] = val
		}
		draft.mu.RUnlock()
	}

	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, epochGroupData := range snapshot {
		epochIdx := epochGroupData.EpochIndex
		modelId := epochGroupData.ModelId
		key := epochGroupCacheKey{Epoch: epochIdx, ModelId: modelId}
		if c.inited && (epochIdx == c.current || epochIdx == c.previous) {
			cloned := proto.Clone(&epochGroupData).(*types.EpochGroupData)
			c.m[key] = *cloned
			c.currentDirty = true
			continue
		}
		// Non-current epoch or cache not inited: write through to store.
		_ = k.EpochGroupDataMap.Set(ctx, collections.Join(epochIdx, modelId), epochGroupData)
	}
}

// ReleaseEpochGroupDraftFromContext releases the draft write lock if held. Call from PostHandler on tx failure so the lock is released when the tx does not commit.
func (k Keeper) ReleaseEpochGroupDraftFromContext(ctx context.Context) {
	draft := getEpochGroupDraftFromContext(ctx)
	if draft == nil || !draft.isWriteLocked() {
		return
	}
	draft.releaseWriteLock()
}

// mergeEpochGroupBranchDraftIntoParent snapshots the branch draft from branchCtx, releases its write lock,
// and merges all entries into the parent's draft (from parentCtx). Call before writeCache() when using
// CacheContextWithEpochGroupBranch so that branch draft writes are visible to the rest of the tx.
func mergeEpochGroupBranchDraftIntoParent(parentCtx, branchCtx context.Context) {
	branchDraft := getEpochGroupBranchDraftFromContext(branchCtx)
	if branchDraft == nil {
		return
	}
	var snapshot map[epochGroupCacheKey]types.EpochGroupData
	if branchDraft.isWriteLocked() {
		snapshot = make(map[epochGroupCacheKey]types.EpochGroupData, len(branchDraft.m))
		for key, val := range branchDraft.m {
			snapshot[key] = val
		}
		branchDraft.releaseWriteLock()
	} else {
		branchDraft.mu.RLock()
		snapshot = make(map[epochGroupCacheKey]types.EpochGroupData, len(branchDraft.m))
		for key, val := range branchDraft.m {
			snapshot[key] = val
		}
		branchDraft.mu.RUnlock()
	}
	parentDraft := getEpochGroupDraftForWriteFromContext(parentCtx)
	if parentDraft == nil {
		return
	}
	parentDraft.lockWrite()
	defer parentDraft.unlockWrite()
	for key, val := range snapshot {
		cloned := proto.Clone(&val).(*types.EpochGroupData)
		parentDraft.m[key] = *cloned
	}
}

// CacheContextWithEpochGroupBranch returns a cached context that has its own EpochGroupData draft branch.
// Use the returned cacheCtx for speculative work; any SetEpochGroupData/RemoveEpochGroupData on cacheCtx
// go to the branch draft. Call the returned writeCache() on success to merge both the store cache and
// the branch draft into the parent context. If you do not call writeCache(), both store and draft changes
// are discarded. Safe to call writeCache() at most once (idempotent).
func (k Keeper) CacheContextWithEpochGroupBranch(ctx sdk.Context) (sdk.Context, func()) {
	cacheCtx, writeCache := ctx.CacheContext()
	branchLazy := &lazyEpochGroupDraft{}
	cacheCtx = cacheCtx.WithContext(context.WithValue(ctx.Context(), ctxKeyEpochGroupBranchDraft{}, branchLazy))
	var written bool
	return cacheCtx, func() {
		if written {
			return
		}
		written = true
		mergeEpochGroupBranchDraftIntoParent(ctx.Context(), cacheCtx.Context())
		writeCache()
	}
}

// InvalidateEpochGroupCache clears the epoch group block cache. Call from BeginBlock each block.
func (k Keeper) InvalidateEpochGroupCache() {
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inited = false
	c.m = make(map[epochGroupCacheKey]types.EpochGroupData)
	c.currentEpochRequestCount.Store(0)
	c.currentDirty = false
}

// ensureEpochGroupCacheInited lazily inits the block cache (current/previous epoch indices). Caller must hold no cache lock.
func (k Keeper) ensureEpochGroupCacheInited(ctx context.Context) {
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited {
		return
	}
	eff, ok := k.GetEffectiveEpochIndex(ctx)
	if !ok {
		return
	}
	c.current = eff
	if eff > 0 {
		c.previous = eff - 1
	} else {
		c.previous = 0
	}
	c.m = make(map[epochGroupCacheKey]types.EpochGroupData)
	c.inited = true
}

// FlushCurrentEpochGroupCache persists cached EpochGroupData for the current epoch to the store. Call from EndBlock.
// Merges currentEpochRequestCount into the current epoch root group's NumberOfRequests before persisting.
func (k Keeper) FlushCurrentEpochGroupCache(ctx context.Context) {
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited {
		return
	}
	delta := c.currentEpochRequestCount.Swap(0)
	if !c.currentDirty {
		return
	}

	// We persist here if have changes in cache or have increments in currentEpochRequestCount
	rootKey := epochGroupCacheKey{Epoch: c.current, ModelId: ""}
	if val, inCache := c.m[rootKey]; inCache {
		val.NumberOfRequests += delta
		_ = k.EpochGroupDataMap.Set(ctx, collections.Join(rootKey.Epoch, rootKey.ModelId), val)
	} else if delta != 0 {
		val, err := k.EpochGroupDataMap.Get(ctx, collections.Join(rootKey.Epoch, rootKey.ModelId))
		if err == nil {
			val.NumberOfRequests += delta
			_ = k.EpochGroupDataMap.Set(ctx, collections.Join(rootKey.Epoch, rootKey.ModelId), val)
		}
	}

	// We persist here all other cached EpochGroupData for the current epoch
	for key, val := range c.m {
		if key.Epoch == c.current && key != rootKey {
			_ = k.EpochGroupDataMap.Set(ctx, collections.Join(key.Epoch, key.ModelId), val)
		}
	}
	c.currentDirty = false
}

// IncrementCurrentEpochGroupRequestCount increments the per-block atomic counter for the current epoch's NumberOfRequests. Committed to store in EndBlock.
func (k Keeper) IncrementCurrentEpochGroupRequestCount(ctx context.Context) int64 {
	c := k.epochGroupCache
	k.ensureEpochGroupCacheInited(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited {
		return 0
	}
	count := c.currentEpochRequestCount.Add(1)
	c.currentDirty = true
	return count
}

// SetEpochGroupData sets EpochGroupData. When ctx has a tx-scoped draft, current/previous-epoch writes go to the draft only (merged to block cache on tx success, then flushed to store in EndBlock).
// Draft is created lazily on first write so read-only txs do not allocate it.
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	epochIdx := epochGroupData.EpochIndex
	modelId := epochGroupData.ModelId
	key := epochGroupCacheKey{Epoch: epochIdx, ModelId: modelId}
	draft := getEpochGroupDraftForWriteFromContext(ctx)

	if draft != nil {
		k.ensureEpochGroupCacheInited(ctx)
		c := k.epochGroupCache
		c.mu.RLock()
		inited, current, previous := c.inited, c.current, c.previous
		c.mu.RUnlock()
		if inited && (epochIdx == current || epochIdx == previous) {
			cloned := proto.Clone(&epochGroupData).(*types.EpochGroupData)
			draft.lockWrite() // reentrant: same goroutine can write multiple times
			draft.m[key] = *cloned
			// Do not unlock here; lock is released in PostHandler (commit or failure)
			return
		}
		// Non-current/previous: write through to store
		k.EpochGroupDataMap.Set(ctx, collections.Join(epochIdx, modelId), epochGroupData)
		return
	}

	// No draft: update block cache or write through to store
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited || (epochIdx != c.current && epochIdx != c.previous) {
		k.EpochGroupDataMap.Set(ctx, collections.Join(epochIdx, modelId), epochGroupData)
		return
	}
	cloned := proto.Clone(&epochGroupData).(*types.EpochGroupData)
	c.m[key] = *cloned
	c.currentDirty = true
}

// GetEpochGroupData returns EpochGroupData by epoch and model. Uses tx draft first (if present), then block cache for current/previous epoch, then store.
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	k.ensureEpochGroupCacheInited(ctx)
	c := k.epochGroupCache
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}

	draft := getEpochGroupDraftFromContext(ctx)
	if draft != nil {
		c.mu.RLock()
		inited, current, previous := c.inited, c.current, c.previous
		c.mu.RUnlock()
		if inited && (epochIndex == current || epochIndex == previous) {
			var cached types.EpochGroupData
			var ok bool
			if draft.isWriteLocked() && atomic.LoadInt64(&draft.writeHolder) == getGID() {
				cached, ok = draft.m[key]
			} else {
				draft.mu.RLock()
				cached, ok = draft.m[key]
				draft.mu.RUnlock()
			}
			if ok {
				cloned := proto.Clone(&cached).(*types.EpochGroupData)
				return *cloned, true
			}
		}
	}

	c.mu.RLock()
	if c.inited && (epochIndex == c.current || epochIndex == c.previous) {
		if cached, ok := c.m[key]; ok {
			c.mu.RUnlock()
			cloned := proto.Clone(&cached).(*types.EpochGroupData)
			return *cloned, true
		}
	}
	c.mu.RUnlock()

	val, err := k.EpochGroupDataMap.Get(ctx, collections.Join(epochIndex, modelId))
	if err != nil {
		return val, false
	}
	c.mu.Lock()
	if c.inited && (epochIndex == c.current || epochIndex == c.previous) {
		cloned := proto.Clone(&val).(*types.EpochGroupData)
		c.m[key] = *cloned
	}
	c.mu.Unlock()
	return val, true
}

// RemoveEpochGroupData removes EpochGroupData from store and from block cache / tx draft when applicable.
// Draft is created only when removing a current/previous-epoch key (same lazy rule as SetEpochGroupData).
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId))
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}
	k.ensureEpochGroupCacheInited(ctx)
	c := k.epochGroupCache
	c.mu.RLock()
	needDraft := c.inited && (epochIndex == c.current || epochIndex == c.previous)
	c.mu.RUnlock()
	if needDraft {
		if draft := getEpochGroupDraftForWriteFromContext(ctx); draft != nil {
			draft.lockWrite()
			delete(draft.m, key)
			// Do not unlock here; released in PostHandler
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited && (epochIndex == c.current || epochIndex == c.previous) {
		delete(c.m, key)
	}
}

// GetAllEpochGroupData returns all EpochGroupData from the store.
func (k Keeper) GetAllEpochGroupData(ctx context.Context) (list []types.EpochGroupData) {
	iter, err := k.EpochGroupDataMap.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	epochGroupDataList, err := iter.Values()
	if err != nil {
		return nil
	}
	return epochGroupDataList
}
