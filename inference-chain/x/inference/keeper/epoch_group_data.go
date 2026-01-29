package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

// ensureEpochGroupCacheInited lazily inits the hot cache from store (current/previous effective epoch).
// Entries are not preloaded; they are filled on first GetEpochGroupData for each (epoch, modelId). Caller must hold no cache lock.
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

// refreshEpochGroupCache updates current/previous to the new effective epoch and drops only the old previous epoch from cache.
// Cache for the old current epoch is kept (it becomes the new previous). Call after SetEffectiveEpochIndex.
func (k Keeper) refreshEpochGroupCache(newEffective uint64) {
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	oldPrevious := c.previous
	c.current = newEffective
	if newEffective > 0 {
		c.previous = newEffective - 1
	} else {
		c.previous = 0
	}
	for key := range c.m {
		if key.Epoch == oldPrevious {
			delete(c.m, key)
		}
	}
	c.inited = true
}

// SetEpochGroupData set a specific epochGroupData in the store from its index and updates the hot cache when the epoch is current or previous.
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	k.EpochGroupDataMap.Set(ctx, collections.Join(epochGroupData.EpochIndex, epochGroupData.ModelId), epochGroupData)
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited {
		return
	}

	if epochIdx != c.current {
		// Non-current epochs are written through directly without caching.
		k.EpochGroupDataMap.Set(ctx, collections.Join(epochIdx, modelId), epochGroupData)
		return
	}
	key := epochGroupCacheKey{Epoch: epochIdx, ModelId: modelId}
	cloned := proto.Clone(&epochGroupData).(*types.EpochGroupData)
	c.m[key] = *cloned
}

// GetEpochGroupData returns a epochGroupData from its index, using the hot cache for current/previous effective epoch.
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	k.ensureEpochGroupCacheInited(ctx)
	c := k.epochGroupCache
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}
	// Try cache for current/previous epoch
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
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId))
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited && (epochIndex == c.current || epochIndex == c.previous) {
		delete(c.m, epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId})
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
