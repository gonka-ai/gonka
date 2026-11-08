package accounting

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"devshard/cmd/gateway/filters"
	"devshard/types"
)

func finishedInference(inputTokens, outputTokens uint64) map[uint64]*types.InferenceRecord {
	return map[uint64]*types.InferenceRecord{
		5: {
			Status: types.StatusFinished, ExecutorSlot: 1, InputLength: 400, MaxTokens: 512,
			InputTokens: inputTokens, OutputTokens: outputTokens, ReservedCost: 900, ActualCost: 300,
		},
	}
}

func moneyOf(t *testing.T, book *Book) SlotMoney {
	t.Helper()
	var money SlotMoney
	for _, record := range book.Query(QueryFilter{}) {
		money.add(SlotMoney{
			Reserved:       record.ReservedCost,
			Actual:         record.ActualCost,
			Refunded:       record.RefundedCost,
			EstimatedInput: record.EstimatedInput,
			EstimatedError: record.EstimatedError,
			TokenizedInput: record.TokenizedInput,
			TokenizedError: record.TokenizedError,
			MaxTokens:      record.MaxTokens,
			Input:          record.InputTokens,
			CountedNonces:  record.CountedNonces,
		})
	}
	return money
}

// Test flow:
//  1. Observe the latest nonce and a finished inference on the escrow, then retire the escrow.
//  2. Read the money totals before and after saving and reloading the book.
//  3. Assert the totals before the restart show 100 input tokens and 1 counted nonce.
//  4. Assert the totals after the restart equal the totals before it.
func TestTheTokensOfARetiredEscrowSurviveARestart(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 8); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.ObserveInferences(testEscrow, finishedInference(100, 42)); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	book.RetireEscrow(testEscrow)

	before := moneyOf(t, book)
	after := moneyOf(t, saveAndReload(t, book, openTestStore(t)))

	if before.Input != 100 || before.CountedNonces != 1 {
		t.Fatalf("before the restart input=%d counted=%d, want 100 and 1", before.Input, before.CountedNonces)
	}
	if after != before {
		t.Fatalf("after the restart %+v, want %+v", after, before)
	}
}

// Test flow:
//  1. Observe a finished inference with 100 input and 42 output tokens, then save and reload the book.
//  2. Observe the same nonce again on the restored book, this time with 55 output tokens.
//  3. Read the money totals.
//  4. Assert input tokens, max tokens and counted nonces reflect the fresh reading rather than adding to the saved fold.
func TestAChainReadingReplacesTheSavedTokensRatherThanAddingToThem(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveInferences(testEscrow, finishedInference(100, 42)); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	restored := saveAndReload(t, book, openTestStore(t))

	if err := restored.ObserveInferences(testEscrow, finishedInference(100, 55)); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	money := moneyOf(t, restored)
	if money.Input != 100 || money.MaxTokens != 512 || money.CountedNonces != 1 {
		t.Fatalf("input=%d max_tokens=%d counted=%d, want 100, 512 and 1", money.Input, money.MaxTokens, money.CountedNonces)
	}
}

// Test flow:
//  1. Observe a finished inference with 100 input tokens, then save and reload the book.
//  2. Observe the restored escrow with no inferences, as the chain reads once auto-seal has removed the record.
//  3. Assert the input tokens, max tokens and counted nonce the saved fold carried are still there.
func TestTheTokensOfASealedInferenceSurviveARestart(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveInferences(testEscrow, finishedInference(100, 42)); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	restored := saveAndReload(t, book, openTestStore(t))

	if err := restored.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	money := moneyOf(t, restored)
	if money.Input != 100 || money.MaxTokens != 512 || money.CountedNonces != 1 {
		t.Fatalf("input=%d max_tokens=%d counted=%d, want 100, 512 and 1", money.Input, money.MaxTokens, money.CountedNonces)
	}
}

// Test flow:
//  1. Observe a finished inference with 100 input tokens, then observe the escrow again with no inferences, as auto-seal leaves it.
//  2. Save and reload the book, and observe it once more with no inferences.
//  3. Assert the sealed inference's tokens are counted once, before and after the restart.
func TestASealedInferenceIsCountedOnceAcrossARestart(t *testing.T) {
	book := newTestBook(t, 4)
	for _, inferences := range []map[uint64]*types.InferenceRecord{finishedInference(100, 42), {}} {
		if err := book.ObserveInferences(testEscrow, inferences); err != nil {
			t.Fatalf("ObserveInferences(): %v", err)
		}
	}
	restored := saveAndReload(t, book, openTestStore(t))
	if err := restored.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	for name, money := range map[string]SlotMoney{"before": moneyOf(t, book), "after": moneyOf(t, restored)} {
		if money.Input != 100 || money.CountedNonces != 1 {
			t.Fatalf("%s the restart input=%d counted=%d, want 100 and 1", name, money.Input, money.CountedNonces)
		}
	}
}

// Test flow:
//  1. Observe three inferences on three slots: two finished with different token counts, one timed out with a reserved cost.
//  2. Retire the escrow and build the expected per-slot money for all four slots, including the untouched one.
//  3. For both the live book and a saved-and-reloaded copy of it, compute slot totals.
//  4. Assert every slot's money matches its expected value in both cases.
func TestTheSavedTokensComeBackOnTheSlotThatEarnedThem(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		5: {Status: types.StatusFinished, ExecutorSlot: 1, InputLength: 400, InputTokens: 100, OutputTokens: 42},
		6: {Status: types.StatusFinished, ExecutorSlot: 2, InputLength: 800, InputTokens: 200, OutputTokens: 84},
		7: {Status: types.StatusTimedOut, ExecutorSlot: 3, InputLength: 1200, ReservedCost: 700},
	}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	book.RetireEscrow(testEscrow)
	expected := map[string]SlotMoney{
		participantFor(0): {},
		participantFor(1): {Input: 100, EstimatedInput: filters.EstimatedPromptTokens(400), CountedNonces: 1},
		participantFor(2): {Input: 200, EstimatedInput: filters.EstimatedPromptTokens(800), CountedNonces: 1},
		participantFor(3): {Reserved: 700, Refunded: 700, EstimatedError: filters.EstimatedPromptTokens(1200)},
	}

	for _, book := range []*Book{book, saveAndReload(t, book, openTestStore(t))} {
		for participant, money := range slotTotals(book.Query(QueryFilter{})) {
			if money != expected[participant] {
				t.Fatalf("%s holds %+v, want %+v", participant, money, expected[participant])
			}
		}
	}
}

func slotTotals(records []ParticipantRecord) map[string]SlotMoney {
	totals := make(map[string]SlotMoney)
	for _, record := range records {
		for _, slot := range record.Slots {
			totals[record.Participant] = SlotMoney{
				Reserved: slot.ReservedCost, Actual: slot.ActualCost, Refunded: slot.RefundedCost,
				EstimatedInput: slot.EstimatedInput, EstimatedError: slot.EstimatedError,
				TokenizedInput: slot.TokenizedInput, TokenizedError: slot.TokenizedError,
				MaxTokens: slot.MaxTokens, Input: slot.InputTokens,
				CountedNonces: slot.CountedNonces,
			}
		}
	}
	return totals
}

// Test flow:
//  1. Open a fresh store, record a ghost, save the book, and close the store.
//  2. Drop the `accounting_money` table to simulate a store written before that table existed.
//  3. Reopen the store and load its snapshot.
//  4. Assert the snapshot still holds the one escrow and that escrow's counters.
func TestAStoreWrittenBeforeTheMoneyTableIsKeptRatherThanDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(): %v", err)
	}
	book := newTestBook(t, 4)
	if err := book.RecordGhost(testEscrow, 5, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	dropMoneyTable(t, path)

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() over the older store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if len(snapshot.Escrows) != 1 {
		t.Fatalf("the upgrade left %d escrows, want the one the older store held", len(snapshot.Escrows))
	}
	if len(snapshot.Escrows[0].Counters) == 0 {
		t.Fatal("the upgrade dropped the counters of the escrow it kept")
	}
}

func dropMoneyTable(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening the store as the older build left it: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`DROP TABLE accounting_money`); err != nil {
		t.Fatalf("dropping the money table: %v", err)
	}
}

// Test flow:
//  1. Observe a finished inference with 100 input tokens, then save and reload the book.
//  2. Observe the restored escrow with the same inference still on the chain.
//  3. Assert its tokens are counted once, not once from the save and again from the chain.
func TestALiveInferenceIsCountedOnceAcrossARestart(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveInferences(testEscrow, finishedInference(100, 42)); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	restored := saveAndReload(t, book, openTestStore(t))

	if err := restored.ObserveInferences(testEscrow, finishedInference(100, 42)); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	money := moneyOf(t, restored)
	if money.Input != 100 || money.CountedNonces != 1 {
		t.Fatalf("moneyOf(restored) = input %d, counted %d, want input 100, counted 1", money.Input, money.CountedNonces)
	}
}
