package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// ensure keeper package is used (k from InferenceKeeper is keeper.Keeper)
var _ func(keeper.Keeper) = func(keeper.Keeper) {}

const cacheBranchTestEpoch = 10

// TestCacheContextWithEpochGroupBranch_WriteThenMerge verifies that writes on the branch
// are merged into the parent draft when writeCache() is called, and become visible after
// CommitEpochGroupDraftFromContext (and thus to subsequent reads).
func TestCacheContextWithEpochGroupBranch_WriteThenMerge(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))

	// Seed initial data (no draft; goes to store/block cache)
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 1)

	// Simulate AnteHandler: tx context with main draft
	txCtx := txCtxWithDraft(sdkCtx)

	// Create branch, write only on branch, then merge
	cacheCtx, writeCache := k.CacheContextWithEpochGroupBranch(txCtx)
	branchData := types.EpochGroupData{
		EpochIndex:          cacheBranchTestEpoch,
		ModelId:             "",
		NumberOfRequests:    999,
		MemberSeedSignatures: []*types.SeedSignature{},
	}
	k.SetEpochGroupData(cacheCtx, branchData)
	writeCache()

	// Parent draft should now contain the branch write; commit it to block cache
	k.CommitEpochGroupDraftFromContext(txCtx)

	// Reads on base context should see the merged value (from block cache)
	val, found := k.GetEpochGroupData(sdkCtx, cacheBranchTestEpoch, "")
	require.True(t, found)
	require.Equal(t, int64(999), val.NumberOfRequests)
}

// TestCacheContextWithEpochGroupBranch_NoWriteCache_Discarded verifies that when
// writeCache() is not called, branch draft writes are discarded and the parent
// (and store) remain unchanged.
func TestCacheContextWithEpochGroupBranch_NoWriteCache_Discarded(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))

	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 100)

	txCtx := txCtxWithDraft(sdkCtx)
	cacheCtx, writeCache := k.CacheContextWithEpochGroupBranch(txCtx)
	_ = writeCache // intentionally not called

	// Write only on branch
	branchData := types.EpochGroupData{
		EpochIndex:          cacheBranchTestEpoch,
		ModelId:             "",
		NumberOfRequests:    999,
		MemberSeedSignatures: []*types.SeedSignature{},
	}
	k.SetEpochGroupData(cacheCtx, branchData)

	// Commit parent draft (empty; we never merged branch)
	k.CommitEpochGroupDraftFromContext(txCtx)

	// Should still see original value, not branch value
	val, found := k.GetEpochGroupData(sdkCtx, cacheBranchTestEpoch, "")
	require.True(t, found)
	require.Equal(t, int64(100), val.NumberOfRequests)
}

// TestCacheContextWithEpochGroupBranch_WriteCacheIdempotent verifies that calling
// writeCache() multiple times does not panic and does not double-merge (idempotent).
func TestCacheContextWithEpochGroupBranch_WriteCacheIdempotent(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 0)

	txCtx := txCtxWithDraft(sdkCtx)
	cacheCtx, writeCache := k.CacheContextWithEpochGroupBranch(txCtx)
	k.SetEpochGroupData(cacheCtx, types.EpochGroupData{
		EpochIndex:          cacheBranchTestEpoch,
		ModelId:             "",
		NumberOfRequests:    42,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	writeCache()
	writeCache() // second call must be safe
	writeCache()

	k.CommitEpochGroupDraftFromContext(txCtx)
	val, found := k.GetEpochGroupData(sdkCtx, cacheBranchTestEpoch, "")
	require.True(t, found)
	require.Equal(t, int64(42), val.NumberOfRequests)
}

// TestCacheContextWithEpochGroupBranch_GetSeesBranchDraft verifies that GetEpochGroupData
// on the branch context sees data written with SetEpochGroupData on that same branch.
func TestCacheContextWithEpochGroupBranch_GetSeesBranchDraft(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 1)

	txCtx := txCtxWithDraft(sdkCtx)
	cacheCtx, writeCache := k.CacheContextWithEpochGroupBranch(txCtx)

	// Set on branch
	k.SetEpochGroupData(cacheCtx, types.EpochGroupData{
		EpochIndex:          cacheBranchTestEpoch,
		ModelId:             "",
		NumberOfRequests:    777,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	// Get on branch must see branch draft value before writeCache
	val, found := k.GetEpochGroupData(cacheCtx, cacheBranchTestEpoch, "")
	require.True(t, found)
	require.Equal(t, int64(777), val.NumberOfRequests)

	// Parent (txCtx) should not see it until writeCache
	valParent, foundParent := k.GetEpochGroupData(txCtx, cacheBranchTestEpoch, "")
	require.True(t, foundParent)
	require.Equal(t, int64(1), valParent.NumberOfRequests, "parent should still see seeded value before merge")

	writeCache()
	valParent2, foundParent2 := k.GetEpochGroupData(txCtx, cacheBranchTestEpoch, "")
	require.True(t, foundParent2)
	require.Equal(t, int64(777), valParent2.NumberOfRequests, "parent should see merged value after writeCache")
}

// TestCacheContextWithEpochGroupBranch_ParentAndBranchWrites verifies that when both
// parent and branch write, merge combines them (branch writes merge into parent draft).
func TestCacheContextWithEpochGroupBranch_ParentAndBranchWrites(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 0)

	txCtx := txCtxWithDraft(sdkCtx)
	// Parent writes root group
	k.SetEpochGroupData(txCtx, types.EpochGroupData{
		EpochIndex:          cacheBranchTestEpoch,
		ModelId:             "",
		NumberOfRequests:    100,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	cacheCtx, writeCache := k.CacheContextWithEpochGroupBranch(txCtx)
	// Branch overwrites same key
	k.SetEpochGroupData(cacheCtx, types.EpochGroupData{
		EpochIndex:          cacheBranchTestEpoch,
		ModelId:             "",
		NumberOfRequests:    200,
		MemberSeedSignatures: []*types.SeedSignature{},
	})
	writeCache()

	k.CommitEpochGroupDraftFromContext(txCtx)
	val, found := k.GetEpochGroupData(sdkCtx, cacheBranchTestEpoch, "")
	require.True(t, found)
	require.Equal(t, int64(200), val.NumberOfRequests, "branch write should override parent in same key")
}

// txCtxWithDraft and seedEpochGroupData are in epoch_group_data_concurrent_test.go;
// we reuse them. If this file is run in isolation, ensure same package and helpers.
// Both are in keeper_test so they are available.
