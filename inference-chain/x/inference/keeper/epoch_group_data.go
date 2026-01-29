package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
)

// ensureEpochGroupCacheInited lazily inits the hot cache and invalidates it when current block height > cachedAtHeight
// (e.g. new block or chain sync), so we always read fresh from store after height advance.
// Entries are not preloaded; they are filled on first GetEpochGroupData for each (epoch, modelId). Caller must hold no cache lock.
func (k Keeper) ensureEpochGroupCacheInited(ctx context.Context) {
	c := k.epochGroupCache
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited {
		if height > c.cachedAtHeight {
			// New block or sync: invalidate cache so we read from store (which has the replayed/synced data).
			c.m = make(map[epochGroupCacheKey]types.EpochGroupData)
			c.currentDirty = false
			c.cachedAtHeight = height
			eff, ok := k.GetEffectiveEpochIndex(ctx)
			if ok {
				c.current = eff
				if eff > 0 {
					c.previous = eff - 1
				} else {
					c.previous = 0
				}
			}
		}
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
	c.cachedAtHeight = height
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
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	k.EpochGroupDataMap.Set(ctx, collections.Join(epochGroupData.EpochIndex, epochGroupData.ModelId), epochGroupData)
	c := k.epochGroupCache
	c.mu.Lock()
	defer c.mu.Unlock()
	epochIdx := epochGroupData.EpochIndex
	modelId := epochGroupData.ModelId

	// If cache is not yet initialized (e.g. during genesis), write through immediately.
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
