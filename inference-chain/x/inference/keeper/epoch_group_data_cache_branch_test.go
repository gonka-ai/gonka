package keeper_test

import (
	"sync"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// ensure keeper package is used (k from InferenceKeeper is keeper.Keeper)
var _ func(keeper.Keeper) = func(keeper.Keeper) {}

const cacheBranchTestEpoch = 10

// TestCacheContext_WriteThenMerge verifies that writes on the branch
// are merged into the parent draft when writeCache() is called, and become visible after
// CommitEpochGroupDraftFromContext (and thus to subsequent reads).
func TestCacheContext_WriteThenMerge(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))

	// Seed initial data (no draft; goes to store/block cache)
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 1)

	// Simulate AnteHandler: tx context with main draft
	txCtx := txCtxWithDraft(k, sdkCtx)

	// Create branch, write only on branch, then merge
	cacheCtx, writeCache := k.CacheContext(txCtx)
	branchData := types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     999,
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

// TestCacheContext_NoWriteCache_Discarded verifies that when
// writeCache() is not called, branch draft writes are discarded and the parent
// (and store) remain unchanged.
func TestCacheContext_NoWriteCache_Discarded(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))

	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 100)

	txCtx := txCtxWithDraft(k, sdkCtx)
	cacheCtx, writeCache := k.CacheContext(txCtx)
	_ = writeCache // intentionally not called

	// Write only on branch
	branchData := types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     999,
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

// TestCacheContext_WriteCacheIdempotent verifies that calling
// writeCache() multiple times does not panic and does not double-merge (idempotent).
func TestCacheContext_WriteCacheIdempotent(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 0)

	txCtx := txCtxWithDraft(k, sdkCtx)
	cacheCtx, writeCache := k.CacheContext(txCtx)
	k.SetEpochGroupData(cacheCtx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     42,
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

// TestCacheContext_GetSeesBranchDraft verifies that GetEpochGroupData
// on the branch context sees data written with SetEpochGroupData on that same branch.
func TestCacheContext_GetSeesBranchDraft(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 1)

	txCtx := txCtxWithDraft(k, sdkCtx)
	cacheCtx, writeCache := k.CacheContext(txCtx)

	// Set on branch
	k.SetEpochGroupData(cacheCtx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     777,
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

// TestCacheContext_ParentAndBranchWrites verifies that when both
// parent and branch write, merge combines them (branch writes merge into parent draft).
func TestCacheContext_ParentAndBranchWrites(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 0)

	txCtx := txCtxWithDraft(k, sdkCtx)
	// Parent writes root group
	k.SetEpochGroupData(txCtx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     100,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	cacheCtx, writeCache := k.CacheContext(txCtx)
	// Branch overwrites same key
	k.SetEpochGroupData(cacheCtx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     200,
		MemberSeedSignatures: []*types.SeedSignature{},
	})
	writeCache()

	k.CommitEpochGroupDraftFromContext(txCtx)
	val, found := k.GetEpochGroupData(sdkCtx, cacheBranchTestEpoch, "")
	require.True(t, found)
	require.Equal(t, int64(200), val.NumberOfRequests, "branch write should override parent in same key")
}

// COSMOS_OCC_ENABLED=1 go test ./x/inference/keeper/ -run TestWriteWithoutMerge_Deadlock -timeout 10s -v
func TestWriteWithoutMerge_Deadlock(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 100)

	txCtx := txCtxWithDraft(k, sdkCtx)
	cacheCtx, _ := k.CacheContext(txCtx)

	k.SetEpochGroupData(cacheCtx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     999,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = k.GetEpochGroupData(sdkCtx, cacheBranchTestEpoch, "")
		}(i)
	}

	go func() {
		k.CommitEpochGroupDraftFromContext(txCtx)
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("DEADLOCK")
	}
}

// TestOCC_ConflictDetection validates that when two txs operate in parallel and
// one reads a key that the other writes, DetectEpochGroupConflicts returns the reader.
func TestOCC_ConflictDetection(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 50)

	k.ResetEpochGroupConflictTracker()

	tx1Ctx := txCtxWithDraft(k, sdkCtx)
	tx2Ctx := txCtxWithDraft(k, sdkCtx)
	k.RegisterEpochGroupTx(tx1Ctx)
	k.RegisterEpochGroupTx(tx2Ctx)

	// tx1 reads the key
	val, found := k.GetEpochGroupData(tx1Ctx, cacheBranchTestEpoch, "")
	require.True(t, found)
	require.Equal(t, int64(50), val.NumberOfRequests)

	// tx2 writes the same key
	k.SetEpochGroupData(tx2Ctx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     200,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	conflictedReads, conflictedWrites := k.DetectEpochGroupConflicts()
	require.Len(t, conflictedReads, 1, "tx1 should be a conflicted reader")
	require.Len(t, conflictedWrites, 1, "tx2 should be a conflicted writer")
}

// TestOCC_NoConflictWhenDifferentKeys validates that txs operating on different
// keys produce no conflicts.
func TestOCC_NoConflictWhenDifferentKeys(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 10)
	k.SetEpochGroupData(sdkCtx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "modelB",
		NumberOfRequests:     20,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	k.ResetEpochGroupConflictTracker()

	tx1Ctx := txCtxWithDraft(k, sdkCtx)
	tx2Ctx := txCtxWithDraft(k, sdkCtx)
	k.RegisterEpochGroupTx(tx1Ctx)
	k.RegisterEpochGroupTx(tx2Ctx)

	// tx1 reads root key
	_, _ = k.GetEpochGroupData(tx1Ctx, cacheBranchTestEpoch, "")

	// tx2 writes modelB key (different key)
	k.SetEpochGroupData(tx2Ctx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "modelB",
		NumberOfRequests:     99,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	conflictedReads, conflictedWrites := k.DetectEpochGroupConflicts()
	require.Len(t, conflictedReads, 0, "no read conflict when txs touch different keys")
	require.Len(t, conflictedWrites, 0, "no write conflict when txs touch different keys")
}

// TestOCC_WriteWriteConflict validates that two txs writing the same key are
// both reported as conflicted writers.
func TestOCC_WriteWriteConflict(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 1)

	k.ResetEpochGroupConflictTracker()

	tx1Ctx := txCtxWithDraft(k, sdkCtx)
	tx2Ctx := txCtxWithDraft(k, sdkCtx)
	k.RegisterEpochGroupTx(tx1Ctx)
	k.RegisterEpochGroupTx(tx2Ctx)

	// Both txs write the same key
	k.SetEpochGroupData(tx1Ctx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     100,
		MemberSeedSignatures: []*types.SeedSignature{},
	})
	k.SetEpochGroupData(tx2Ctx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     200,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	conflictedReads, conflictedWrites := k.DetectEpochGroupConflicts()
	require.Len(t, conflictedReads, 0, "no read conflicts — neither tx read")
	require.Len(t, conflictedWrites, 2, "both txs should be conflicted writers (write-write on same key)")
}

// TestOCC_MultipleWriteTxs validates conflict detection across 3 txs:
// tx1 reads key A, tx2 writes key A, tx3 writes key A.
// Expected: tx1 is a conflicted reader, tx2 and tx3 are conflicted writers (read-write + write-write).
func TestOCC_MultipleWriteTxs(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 10)

	k.ResetEpochGroupConflictTracker()

	tx1Ctx := txCtxWithDraft(k, sdkCtx)
	tx2Ctx := txCtxWithDraft(k, sdkCtx)
	tx3Ctx := txCtxWithDraft(k, sdkCtx)
	k.RegisterEpochGroupTx(tx1Ctx)
	k.RegisterEpochGroupTx(tx2Ctx)
	k.RegisterEpochGroupTx(tx3Ctx)

	// tx1 reads
	_, _ = k.GetEpochGroupData(tx1Ctx, cacheBranchTestEpoch, "")

	// tx2 writes the same key
	k.SetEpochGroupData(tx2Ctx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     50,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	// tx3 also writes the same key
	k.SetEpochGroupData(tx3Ctx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     75,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	conflictedReads, conflictedWrites := k.DetectEpochGroupConflicts()
	require.Len(t, conflictedReads, 1, "tx1 is the only reader that conflicts")
	require.Len(t, conflictedWrites, 2, "tx2 and tx3 are both conflicted writers (read-write from tx1 + write-write between them)")
}

// TestOCC_ResetClearsTracker validates that ResetEpochGroupConflictTracker
// clears all prior read/write sets.
func TestOCC_ResetClearsTracker(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 1)

	tx1Ctx := txCtxWithDraft(k, sdkCtx)
	tx2Ctx := txCtxWithDraft(k, sdkCtx)
	k.RegisterEpochGroupTx(tx1Ctx)
	k.RegisterEpochGroupTx(tx2Ctx)

	_, _ = k.GetEpochGroupData(tx1Ctx, cacheBranchTestEpoch, "")
	k.SetEpochGroupData(tx2Ctx, types.EpochGroupData{
		EpochIndex:           cacheBranchTestEpoch,
		ModelId:              "",
		NumberOfRequests:     5,
		MemberSeedSignatures: []*types.SeedSignature{},
	})

	conflictedReads, _ := k.DetectEpochGroupConflicts()
	require.Len(t, conflictedReads, 1)

	// After reset, no conflicts
	k.ResetEpochGroupConflictTracker()
	r, w := k.DetectEpochGroupConflicts()
	require.Len(t, r, 0)
	require.Len(t, w, 0)
}

// TestOCC_MultipleReadsNoConflict validates that many txs reading the same key
// without any writer produce zero conflicts (read-read is never a conflict).
func TestOCC_MultipleReadsNoConflict(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, cacheBranchTestEpoch))
	seedEpochGroupData(k, sdkCtx, cacheBranchTestEpoch, 42)

	k.ResetEpochGroupConflictTracker()

	const numReaders = 10
	txCtxs := make([]sdk.Context, numReaders)
	for i := 0; i < numReaders; i++ {
		txCtxs[i] = txCtxWithDraft(k, sdkCtx)
		k.RegisterEpochGroupTx(txCtxs[i])
	}

	for i := 0; i < numReaders; i++ {
		val, found := k.GetEpochGroupData(txCtxs[i], cacheBranchTestEpoch, "")
		require.True(t, found, "reader %d", i)
		require.Equal(t, int64(42), val.NumberOfRequests, "reader %d", i)
	}

	conflictedReads, conflictedWrites := k.DetectEpochGroupConflicts()
	require.Len(t, conflictedReads, 0, "read-read should never conflict")
	require.Len(t, conflictedWrites, 0, "no writers means no write conflicts")
}

// txCtxWithDraft and seedEpochGroupData are in epoch_group_data_concurrent_test.go;
// we reuse them. If this file is run in isolation, ensure same package and helpers.
// Both are in keeper_test so they are available.
