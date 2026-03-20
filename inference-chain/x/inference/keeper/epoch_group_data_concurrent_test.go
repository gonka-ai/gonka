package keeper_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

var _ = strconv.IntSize

const (
	concurrentTestEpoch   = 5
	concurrentNumReaders = 20
	concurrentNumWriters = 5
)

// txCtxWithDraft returns an sdk.Context that has a tx-scoped draft attached (simulating AnteHandler).
func txCtxWithDraft(k keeper.Keeper, sdkCtx sdk.Context) sdk.Context {
	stdCtx := sdkCtx.Context()
	if stdCtx == nil {
		stdCtx = context.Background()
	}
	stdCtx = k.EpochGroupStore().WithDraft(stdCtx)
	return sdkCtx.WithContext(stdCtx)
}

// seedEpochGroupData writes one root group for the test epoch into the store (no draft).
func seedEpochGroupData(k keeper.Keeper, ctx sdk.Context, epoch uint64, numberOfRequests int64) {
	data := types.EpochGroupData{
		EpochIndex:          epoch,
		ModelId:             "",
		NumberOfRequests:    numberOfRequests,
		MemberSeedSignatures: []*types.SeedSignature{},
	}
	k.SetEpochGroupData(ctx, data)
}

// TestConcurrentReadsSameResult runs many read-only "transactions" in parallel.
// All use the same epoch key; no draft. They must all see the same value and run without blocking each other.
func TestConcurrentReadsSameResult(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	_ = k.SetEffectiveEpochIndex(sdkCtx, concurrentTestEpoch)
	seedEpochGroupData(k, sdkCtx, concurrentTestEpoch, 100)

	var wg sync.WaitGroup
	results := make([]types.EpochGroupData, concurrentNumReaders)
	for i := 0; i < concurrentNumReaders; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val, found := k.GetEpochGroupData(sdkCtx, concurrentTestEpoch, "")
			require.True(t, found)
			results[idx] = val
		}(i)
	}
	wg.Wait()

	for i := 0; i < concurrentNumReaders; i++ {
		require.Equal(t, int64(100), results[i].NumberOfRequests, "reader %d", i)
		require.Equal(t, uint64(concurrentTestEpoch), results[i].EpochIndex)
	}
}

// TestOneWriterManyReaders_ReadersSeeWriteAfterCommit: one writer tx (Set + Commit), then many readers.
// Readers that run after the writer must see the written value (block cache updated by Commit).
func TestOneWriterManyReaders_ReadersSeeWriteAfterCommit(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	_ = k.SetEffectiveEpochIndex(sdkCtx, concurrentTestEpoch)
	seedEpochGroupData(k, sdkCtx, concurrentTestEpoch, 1)

	writerDone := make(chan struct{})
	go func() {
		txCtx := txCtxWithDraft(k, sdkCtx)
		data := types.EpochGroupData{
			EpochIndex:          concurrentTestEpoch,
			ModelId:             "",
			NumberOfRequests:    999,
			MemberSeedSignatures: []*types.SeedSignature{},
		}
		k.SetEpochGroupData(txCtx, data)
		k.CommitEpochGroupDraftFromContext(txCtx)
		close(writerDone)
	}()

	<-writerDone

	var wg sync.WaitGroup
	for i := 0; i < concurrentNumReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, found := k.GetEpochGroupData(sdkCtx, concurrentTestEpoch, "")
			require.True(t, found)
			require.Equal(t, int64(999), val.NumberOfRequests)
		}()
	}
	wg.Wait()
}

// TestOneWriterMultipleSetGet_NoDeadlock: one writer performs multiple Set/Get in one "tx" (reentrant draft lock),
// while many readers run in parallel. Must not deadlock (run with -race and timeout).
func TestOneWriterMultipleSetGet_NoDeadlock(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	_ = k.SetEffectiveEpochIndex(sdkCtx, concurrentTestEpoch)
	seedEpochGroupData(k, sdkCtx, concurrentTestEpoch, 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		txCtx := txCtxWithDraft(k, sdkCtx)
		for i := 0; i < 10; i++ {
			data := types.EpochGroupData{
				EpochIndex:          concurrentTestEpoch,
				ModelId:             "",
				NumberOfRequests:    int64(i),
				MemberSeedSignatures: []*types.SeedSignature{},
			}
			k.SetEpochGroupData(txCtx, data)
			_, _ = k.GetEpochGroupData(txCtx, concurrentTestEpoch, "")
		}
		k.CommitEpochGroupDraftFromContext(txCtx)
	}()

	var wg sync.WaitGroup
	for i := 0; i < concurrentNumReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_, _ = k.GetEpochGroupData(sdkCtx, concurrentTestEpoch, "")
			}
		}()
	}
	wg.Wait()

	select {
	case <-done:
		// writer finished
	case <-time.After(5 * time.Second):
		t.Fatal("writer or readers deadlocked (timeout)")
	}
}

// TestMultipleWritersMultipleReaders: several writers commit in sequence (each takes block cache lock on Commit),
// many readers run in parallel. After all writers finish, the last written value must be visible to readers.
func TestMultipleWritersMultipleReaders_LastWriteWins(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)
	_ = k.SetEffectiveEpochIndex(sdkCtx, concurrentTestEpoch)
	seedEpochGroupData(k, sdkCtx, concurrentTestEpoch, 0)

	// Writers commit one after another so "last" is well-defined
	var writerOrder sync.Mutex
	var lastCommittedValue int64

	var wgWriters sync.WaitGroup
	for w := 0; w < concurrentNumWriters; w++ {
		val := int64(w + 1)
		wgWriters.Add(1)
		go func() {
			defer wgWriters.Done()
			txCtx := txCtxWithDraft(k, sdkCtx)
			data := types.EpochGroupData{
				EpochIndex:          concurrentTestEpoch,
				ModelId:             "",
				NumberOfRequests:    val,
				MemberSeedSignatures: []*types.SeedSignature{},
			}
			k.SetEpochGroupData(txCtx, data)
			writerOrder.Lock()
			k.CommitEpochGroupDraftFromContext(txCtx)
			lastCommittedValue = val
			writerOrder.Unlock()
		}()
	}

	var wgReaders sync.WaitGroup
	readerResults := make([]int64, concurrentNumReaders)
	for i := 0; i < concurrentNumReaders; i++ {
		wgReaders.Add(1)
		go func(idx int) {
			defer wgReaders.Done()
			// Readers run while writers are still committing
			v, found := k.GetEpochGroupData(sdkCtx, concurrentTestEpoch, "")
			if found {
				readerResults[idx] = v.NumberOfRequests
			}
		}(i)
	}

	wgWriters.Wait()
	wgReaders.Wait()

	// Final read must see the last committed value (last writer to hold the lock)
	finalVal, found := k.GetEpochGroupData(sdkCtx, concurrentTestEpoch, "")
	require.True(t, found)
	require.Equal(t, lastCommittedValue, finalVal.NumberOfRequests,
		"final read should see last committed value %d", lastCommittedValue)

	// Every reader must have seen a value in [1, concurrentNumWriters] (some snapshot)
	for i := 0; i < concurrentNumReaders; i++ {
		if readerResults[i] != 0 {
			require.GreaterOrEqual(t, readerResults[i], int64(1))
			require.LessOrEqual(t, readerResults[i], int64(concurrentNumWriters))
		}
	}
}
