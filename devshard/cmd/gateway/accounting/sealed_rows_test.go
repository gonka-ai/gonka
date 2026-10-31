package accounting

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Test flow:
//  1. Assign nonce 4 and nonce 5; report nonce 4's race as won and record an execution timeout on nonce 5.
//  2. Observe the chain holding nonce 4 finished and nonce 5 started, then observe both sealed away.
//  3. Assert the counters a query reads are the same before and after the seal.
//  4. Assert the sealed, finished nonce 4 holds no row while the revisable nonce 5 keeps its row and stays listed unfinished.
func TestASealedNonceWithAFinalDispositionLeavesNoRow(t *testing.T) {
	book := newTestBook(t, 4)
	require.NoError(t, book.RecordAssigned(testEscrow, 4, "req-a"))
	require.NoError(t, book.RecordAssigned(testEscrow, 5, "req-b"))
	require.NoError(t, book.RecordRace(testEscrow, []Attempt{{Nonce: 4, RequestID: "req-a", Sent: true, Usage: UsageWinner, Terminal: "won"}}))
	require.NoError(t, book.RecordTimeout(testEscrow, 5, "execution", "completed", "none"))
	require.NoError(t, book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		4: {Status: types.StatusFinished},
		5: {Status: types.StatusStarted},
	}))
	beforeSeal := book.Query(QueryFilter{})

	require.NoError(t, book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}))
	afterSeal := book.Query(QueryFilter{})

	require.Equal(t, beforeSeal, afterSeal)
	require.False(t, holdsNonceRow(book, testEscrow, 4))
	require.True(t, holdsNonceRow(book, testEscrow, 5))
	require.Equal(t, []uint64{5}, book.UnfinishedNonces(testEscrow))
}

func holdsNonceRow(book *Book, escrowID string, nonce uint64) bool {
	book.mu.RLock()
	defer book.mu.RUnlock()
	_, held := book.escrows[escrowID].nonces[nonce]
	return held
}

// Test flow:
//  1. Assign nonce 4 and observe the chain holding it validated, so the ledger counts it finished with its usage unknown.
//  2. Observe it sealed away before its race reports.
//  3. Report its race as won, then mark finished every nonce the ledger still lists unfinished, as the recorder's sealed close does.
//  4. Assert the nonce is counted once, as finished and used, and its row is gone.
func TestARaceThatReportsAfterItsNonceSealedCountsTheNonceOnce(t *testing.T) {
	book := newTestBook(t, 4)
	require.NoError(t, book.RecordAssigned(testEscrow, 4, "req-a"))
	require.NoError(t, book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		4: {Status: types.StatusValidated},
	}))
	require.NoError(t, book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}))

	require.NoError(t, book.RecordRace(testEscrow, []Attempt{{Nonce: 4, RequestID: "req-a", Sent: true, Usage: UsageWinner, Terminal: "won"}}))
	require.NoError(t, book.MarkFinished(testEscrow, book.UnfinishedNonces(testEscrow)))

	require.Equal(t, map[Disposition]uint64{DispositionFinishedUsed: 1}, countedDispositions(book, testEscrow))
	require.False(t, holdsNonceRow(book, testEscrow, 4))
}

// Test flow:
//  1. Assign nonce 4, report its race with its usage unknown, and observe the chain holding it finished.
//  2. Observe it sealed away.
//  3. Assert its row is gone and it stays counted once, as finished with its usage unknown.
func TestASealedNonceWhoseRaceReportedLeavesNoRow(t *testing.T) {
	book := newTestBook(t, 4)
	require.NoError(t, book.RecordAssigned(testEscrow, 4, "req-a"))
	require.NoError(t, book.RecordRace(testEscrow, []Attempt{{Nonce: 4, RequestID: "req-a", Sent: true, Usage: UsageUnknown, Terminal: "failed"}}))
	require.NoError(t, book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		4: {Status: types.StatusFinished},
	}))

	require.NoError(t, book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}))

	require.False(t, holdsNonceRow(book, testEscrow, 4))
	require.Equal(t, map[Disposition]uint64{DispositionFinishedUsageUnknown: 1}, countedDispositions(book, testEscrow))
}

// Test flow:
//  1. Assign nonce 4 and save and reload the book, as a restart does, so its race is gone with the process.
//  2. Observe the chain holding it validated, then observe it sealed away.
//  3. Assert its row is gone and it is counted once, as finished with its usage unknown.
func TestARestoredNonceThatFinishesLeavesNoRowOnceSealed(t *testing.T) {
	book := newTestBook(t, 4)
	require.NoError(t, book.RecordAssigned(testEscrow, 4, "req-a"))
	restored := saveAndReload(t, book, openTestStore(t))

	require.NoError(t, restored.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		4: {Status: types.StatusValidated},
	}))
	require.NoError(t, restored.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}))

	require.False(t, holdsNonceRow(restored, testEscrow, 4))
	require.Equal(t, map[Disposition]uint64{DispositionFinishedUsageUnknown: 1}, countedDispositions(restored, testEscrow))
}

func countedDispositions(book *Book, escrowID string) map[Disposition]uint64 {
	book.mu.RLock()
	defer book.mu.RUnlock()
	counted := make(map[Disposition]uint64)
	for key, count := range book.escrows[escrowID].counters {
		counted[key.Disposition] += uint64(count)
	}
	return counted
}
