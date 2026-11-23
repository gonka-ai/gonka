package accounting

import (
	"slices"
	"testing"

	"devshard/types"
)

func startedInference() map[uint64]*types.InferenceRecord {
	return map[uint64]*types.InferenceRecord{
		5: {Status: types.StatusStarted, ExecutorSlot: 1, InputLength: 400, MaxTokens: 512, ReservedCost: 900},
	}
}

func sealedFinished(inputTokens uint64) types.InferenceRecord {
	return *finishedInference(inputTokens, 42)[5]
}

// Test flow:
//  1. Observe nonce 5 started and nonce 6 started on the escrow.
//  2. Ask which costs the next reading folds against a window that still holds nonce 6.
//  3. Assert only nonce 5 is listed, and an escrow the ledger never opened lists none.
func TestOnlyACostThatLeftTheLiveWindowIsListedToFold(t *testing.T) {
	book := newTestBook(t, 4)
	inferences := startedInference()
	inferences[6] = &types.InferenceRecord{Status: types.StatusStarted, ExecutorSlot: 2, InputLength: 100, ReservedCost: 300}
	if err := book.ObserveInferences(testEscrow, inferences); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	leaving := book.NoncesToFold(testEscrow, map[uint64]*types.InferenceRecord{6: inferences[6]})

	if !slices.Equal(leaving, []uint64{5}) {
		t.Fatalf("NoncesToFold() = %v, want [5]", leaving)
	}
	if unknown := book.NoncesToFold("escrow-never-opened", nil); unknown != nil {
		t.Fatalf("NoncesToFold(unknown escrow) = %v, want nil", unknown)
	}
}

// Test flow:
//  1. Observe nonce 5 started, then hand the ledger sealed records for nonce 5 (finished, 100 input tokens) and nonce 6, which it never read.
//  2. Observe the escrow with no inferences, as the chain reads once the seal removed nonce 5, then repeat the sealed records and the reading.
//  3. Assert the money counts nonce 5 once with its sealed input tokens and no estimated error, and nonce 6 not at all.
func TestASealedRecordReplacesOnlyACostTheLedgerStillHolds(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveInferences(testEscrow, startedInference()); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	sealed := map[uint64]types.InferenceRecord{5: sealedFinished(100), 6: sealedFinished(70)}

	for range 2 {
		if err := book.ObserveSealedRecords(testEscrow, sealed); err != nil {
			t.Fatalf("ObserveSealedRecords(): %v", err)
		}
		if err := book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}); err != nil {
			t.Fatalf("ObserveInferences(): %v", err)
		}
	}

	money := moneyOf(t, book)
	if money.Input != 100 || money.CountedNonces != 1 || money.EstimatedError != 0 {
		t.Fatalf("input=%d counted=%d estimated_error=%d, want 100, 1 and 0", money.Input, money.CountedNonces, money.EstimatedError)
	}
}

// Test flow:
//  1. Observe nonce 5 started, then save and reload the book, as a restart whose last save predates the finish.
//  2. Hand the restored book nonce 5's sealed record and observe the escrow with no inferences.
//  3. Assert the money counts nonce 5 with its sealed input tokens and no estimated error.
func TestARestoredStartedCostFoldsTheRecordKeptAtSeal(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveInferences(testEscrow, startedInference()); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	restored := saveAndReload(t, book, openTestStore(t))

	if err := restored.ObserveSealedRecords(testEscrow, map[uint64]types.InferenceRecord{5: sealedFinished(100)}); err != nil {
		t.Fatalf("ObserveSealedRecords(): %v", err)
	}
	if err := restored.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	money := moneyOf(t, restored)
	if money.Input != 100 || money.CountedNonces != 1 || money.EstimatedError != 0 {
		t.Fatalf("input=%d counted=%d estimated_error=%d, want 100, 1 and 0", money.Input, money.CountedNonces, money.EstimatedError)
	}
}
