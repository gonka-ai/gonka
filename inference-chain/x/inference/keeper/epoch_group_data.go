package keeper

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"cosmossdk.io/collections"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

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

// epochGroupDraft holds tx-scoped EpochGroupData writes for optimistic parallel execution.
// Uses a reentrant write lock: the same goroutine can write (or delete) multiple times; lock is released only in PostHandler (commit or failure).
type epochGroupDraft struct {
	mu          sync.RWMutex
	m           map[epochGroupCacheKey]types.EpochGroupData
	writeHolder int64 // goroutine id of the writer (for reentrancy)
	writeCount  int32 // number of Lock() calls not yet Unlock()ed by the holder
}

// lockWrite acquires the write lock; reentrant for the same goroutine (increments count). Release via unlockWrite or releaseWriteLock.
func (d *epochGroupDraft) lockWrite() {
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
func (d *epochGroupDraft) unlockWrite() {
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

// WithEpochGroupDraft attaches a new tx-scoped draft to ctx. Call at tx start (e.g. from AnteHandler).
func WithEpochGroupDraft(ctx context.Context) context.Context {
	d := &epochGroupDraft{m: make(map[epochGroupCacheKey]types.EpochGroupData)}
	return context.WithValue(ctx, ctxKeyEpochGroupDraft{}, d)
}

// getEpochGroupDraftFromContext returns the draft from ctx, or nil if no draft is bound.
func getEpochGroupDraftFromContext(ctx context.Context) *epochGroupDraft {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(ctxKeyEpochGroupDraft{})
	if v == nil {
		return nil
	}
	d, _ := v.(*epochGroupDraft)
	return d
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
	if !c.currentDirty && delta == 0 {
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
func (k Keeper) IncrementCurrentEpochGroupRequestCount(ctx context.Context) {
	c := k.epochGroupCache
	k.ensureEpochGroupCacheInited(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited {
		return
	}
	c.currentEpochRequestCount.Add(1)
	c.currentDirty = true
}

// SetEpochGroupData sets EpochGroupData. When ctx has a tx-scoped draft, current/previous-epoch writes go to the draft only (merged to block cache on tx success, then flushed to store in EndBlock).
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	epochIdx := epochGroupData.EpochIndex
	modelId := epochGroupData.ModelId
	key := epochGroupCacheKey{Epoch: epochIdx, ModelId: modelId}
	draft := getEpochGroupDraftFromContext(ctx)

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
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId))
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}
	if draft := getEpochGroupDraftFromContext(ctx); draft != nil {
		draft.lockWrite()
		delete(draft.m, key)
		// Do not unlock here; released in PostHandler
	}
	c := k.epochGroupCache
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
