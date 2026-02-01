package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

// ctxKeyEpochGroupDraft is the context key for the tx-scoped EpochGroupData draft map.
// Using a private type ensures only this package can set/read the value.
type ctxKeyEpochGroupDraft struct{}

// WithEpochGroupDraft attaches a new tx-scoped draft map to ctx. Call at tx start (e.g. from AnteHandler).
// Compatible with parallel/optimistic execution: each tx gets its own draft in its context.
// Returns a new context; use it for the rest of the tx (e.g. return from AnteHandler if the SDK allows
// deriving sdk.Context from context.Context, or use ctx.WithContext(newCtx) if the SDK provides it).
func WithEpochGroupDraft(ctx context.Context) context.Context {
	draft := make(map[epochGroupCacheKey]types.EpochGroupData)
	return context.WithValue(ctx, ctxKeyEpochGroupDraft{}, &draft)
}

// getEpochGroupDraftFromContext returns the draft map from ctx, or nil if no draft is bound.
func getEpochGroupDraftFromContext(ctx context.Context) map[epochGroupCacheKey]types.EpochGroupData {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(ctxKeyEpochGroupDraft{})
	if v == nil {
		return nil
	}
	m, _ := v.(*map[epochGroupCacheKey]types.EpochGroupData)
	if m == nil {
		return nil
	}
	return *m
}

// CommitEpochGroupDraftFromContext merges the draft from ctx into the real epoch group cache.
// Call from PostHandler on tx success. Does not write to store; FlushCurrentEpochGroupCache in EndBlock persists.
// If ctx has no draft, no-op. Safe for parallel execution: each tx's context holds its own draft.
func (k *Keeper) CommitEpochGroupDraftFromContext(ctx context.Context) {
	draft := getEpochGroupDraftFromContext(ctx)
	if draft == nil {
		return
	}

	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, epochGroupData := range draft {
		epochIdx := epochGroupData.EpochIndex
		modelId := epochGroupData.ModelId
		key := epochGroupCacheKey{Epoch: epochIdx, ModelId: modelId}
		if c.inited && (epochIdx == c.current || epochIdx == c.previous) {
			cloned := proto.Clone(&epochGroupData).(*types.EpochGroupData)
			c.m[key] = *cloned
			c.currentDirty = true
			continue
		}
		// Non-current epoch or cache is not inited, write through to store (no cache).
		k.EpochGroupDataMap.Set(ctx, collections.Join(epochIdx, modelId), epochGroupData)
	}
}

// InvalidateEpochGroupCache clears the epoch group cache. Call from PrepareForBlock so the cache
// is invalidated once per block and the next Get/Set will re-init from store.
func (k *Keeper) InvalidateEpochGroupCache() {
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inited = false
	c.m = make(map[epochGroupCacheKey]types.EpochGroupData)
	c.currentDirty = false
}

// ensureEpochGroupCacheInited lazily inits the hot cache. Entries are not preloaded; they are
// filled on first GetEpochGroupData for each (epoch, modelId). Caller must hold no cache lock.
// Cache is invalidated in PrepareForBlock, so after a new block this will re-init from store.
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

// FlushCurrentEpochGroupCache persists all cached EpochGroupData entries for the current epoch to the store.
// This should be called from EndBlock so that batched in-block updates are written once per block.
func (k Keeper) FlushCurrentEpochGroupCache(ctx context.Context) {
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited || !c.currentDirty {
		return
	}
	for key, val := range c.m {
		if key.Epoch == c.current {
			// Persist current-epoch entries to the underlying store.
			_ = k.EpochGroupDataMap.Set(ctx, collections.Join(key.Epoch, key.ModelId), val)
		}
	}
	c.currentDirty = false
}

// SetEpochGroupData set a specific epochGroupData in the store from its index and updates the hot cache when the epoch is current or previous.
// When ctx has a tx-scoped draft (from WithEpochGroupDraft), current-epoch writes go to the draft only; they are merged in PostHandler on success.
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	k.EpochGroupDataMap.Set(ctx, collections.Join(epochGroupData.EpochIndex, epochGroupData.ModelId), epochGroupData)
	c := k.epochGroupCache
	draft := getEpochGroupDraftFromContext(ctx)
	epochIdx := epochGroupData.EpochIndex
	modelId := epochGroupData.ModelId
	key := epochGroupCacheKey{Epoch: epochIdx, ModelId: modelId}

	// If ctx has a draft, current-epoch writes go to draft only.
	if draft != nil {
		cloned := proto.Clone(&epochGroupData).(*types.EpochGroupData)
		draft[key] = *cloned
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// If cache is not yet initialized (e.g. during genesis) or not current epoch, write through immediately.
	if !c.inited || epochIdx != c.current || epochIdx != c.previous {
		k.EpochGroupDataMap.Set(ctx, collections.Join(epochIdx, modelId), epochGroupData)
		return
	}

	cloned := proto.Clone(&epochGroupData).(*types.EpochGroupData)
	c.m[key] = *cloned
	c.currentDirty = true
}

// GetEpochGroupData returns a epochGroupData from its index, using the hot cache for current/previous effective epoch.
// When ctx has a tx-scoped draft, the draft is checked first for current/previous epoch keys.
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	k.ensureEpochGroupCacheInited(ctx)
	c := k.epochGroupCache
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}

	// If ctx has a draft, check draft first for current/previous epoch.
	draft := getEpochGroupDraftFromContext(ctx)
	if draft != nil {
		c.mu.RLock()
		inited, current, previous := c.inited, c.current, c.previous
		c.mu.RUnlock()
		if inited && (epochIndex == current || epochIndex == previous) {
			if cached, ok := draft[key]; ok {
				cloned := proto.Clone(&cached).(*types.EpochGroupData)
				return *cloned, true
			}
		}
	}

	// Try real cache for current/previous epoch
	c.mu.RLock()
	if c.inited && (epochIndex == c.current || epochIndex == c.previous) {
		if cached, ok := c.m[key]; ok {
			c.mu.RUnlock()
			cloned := proto.Clone(&cached).(*types.EpochGroupData)
			return *cloned, true
		}
	}
	c.mu.RUnlock()
	// Load from store
	val, err := k.EpochGroupDataMap.Get(ctx, collections.Join(epochIndex, modelId))

	if err != nil {
		return val, false
	}
	// Backfill cache for current/previous epoch
	c.mu.Lock()
	if c.inited && (epochIndex == c.current || epochIndex == c.previous) {
		cloned := proto.Clone(&val).(*types.EpochGroupData)
		c.m[key] = *cloned
	}
	c.mu.Unlock()
	return val, true
}

// RemoveEpochGroupData removes a epochGroupData from the store and from the hot cache when the epoch is current or previous.
// When ctx has a draft, also removes the key from the draft so it is not re-applied on commit.
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId))
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}
	if draft := getEpochGroupDraftFromContext(ctx); draft != nil {
		delete(draft, key)
	}
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited && (epochIndex == c.current || epochIndex == c.previous) {
		delete(c.m, key)
	}
}

// GetAllEpochGroupData returns all epochGroupData
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
