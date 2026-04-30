package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// ---------- Keeper methods ----------

// SetEpochGroupData writes EpochGroupData through the optimistic store.
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	key := epochGroupCacheKey{Epoch: epochGroupData.EpochIndex, ModelId: epochGroupData.ModelId}
	k.epochGroupStore.Set(ctx, key, epochGroupData)
}

// GetEpochGroupData reads EpochGroupData through the optimistic store.
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}
	return k.epochGroupStore.Get(ctx, key)
}

// RemoveEpochGroupData removes EpochGroupData through the optimistic store.
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	key := epochGroupCacheKey{Epoch: epochIndex, ModelId: modelId}
	k.epochGroupStore.Remove(ctx, key)
}

// CommitEpochGroupDraftFromContext merges the tx draft into the block cache.
// Call from PostHandler on tx success.
func (k Keeper) CommitEpochGroupDraftFromContext(ctx context.Context) {
	k.epochGroupStore.CommitDraft(ctx)
}

// ReleaseEpochGroupDraftFromContext discards the tx draft. Call from PostHandler on tx failure.
func (k Keeper) ReleaseEpochGroupDraftFromContext(ctx context.Context) {
	k.epochGroupStore.ReleaseDraft(ctx)
}

// InvalidateEpochGroupCache clears the block cache and conflict tracker. Call from BeginBlock.
func (k Keeper) InvalidateEpochGroupCache() {
	k.epochGroupStore.Invalidate()
}

// FlushCurrentEpochGroupCache persists block-cache entries to the store. Call from EndBlock.
func (k Keeper) FlushCurrentEpochGroupCache(ctx context.Context) {
	k.epochGroupStore.Flush(ctx)
}

// RegisterEpochGroupTx registers a tx for OCC tracking.
func (k Keeper) RegisterEpochGroupTx(ctx context.Context) {
	k.epochGroupStore.RegisterTx(ctx)
}

// DetectEpochGroupConflicts returns txIDs involved in read-write or write-write collisions.
func (k Keeper) DetectEpochGroupConflicts() (conflictedReads, conflictedWrites []uintptr) {
	return k.epochGroupStore.DetectConflicts()
}

// ResetEpochGroupConflictTracker clears all tracked read/write sets.
func (k Keeper) ResetEpochGroupConflictTracker() {
	k.epochGroupStore.ResetConflictTracker()
}

// CacheContextWithBranch returns a cached context with branch drafts for ALL optimistic stores.
// Call writeCache() on success to merge both the SDK store cache and all branch drafts.
func (k Keeper) CacheContext(ctx sdk.Context) (sdk.Context, func()) {
	return k.storeGroup.CacheContext(ctx)
}

// GetAllEpochGroupData returns all EpochGroupData, preferring block-cache
// contents when available, otherwise falling back to the underlying collection.
func (k Keeper) GetAllEpochGroupData(ctx context.Context) (list []types.EpochGroupData) {
	if vals := k.epochGroupStore.BlockCacheValues(); len(vals) > 0 {
		return vals
	}
	iter, err := k.epochGroupStore.Map.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	epochGroupDataList, err := iter.Values()
	if err != nil {
		return nil
	}
	return epochGroupDataList
}

// EpochGroupStore returns the underlying optimistic store for EpochGroupData.
// Used by the AnteHandler to wire drafts.
func (k Keeper) EpochGroupStore() *OptimisticCollMap[epochGroupCacheKey, collections.Pair[uint64, string], types.EpochGroupData] {
	return k.epochGroupStore
}
