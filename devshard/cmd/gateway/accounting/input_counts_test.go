package accounting

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	"devshard/cmd/gateway/filters"
	"devshard/types"
)

const (
	retiredCountedNonces        = 50_000
	retiredCountsHeapAllowance  = 256 * 1024
	insertMoneyWithoutTokenized = `INSERT INTO accounting_money
		 (escrow_id, slot_id, reserved_cost, actual_cost, refunded_cost, estimated_input, estimated_error,
		  max_tokens, input_tokens, counted_nonces, output_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
)

func countedRaceInferences() map[uint64]*types.InferenceRecord {
	return map[uint64]*types.InferenceRecord{
		5: {Status: types.StatusFinished, ExecutorSlot: 1, InputLength: 400, InputTokens: 100},
		6: {Status: types.StatusFinished, ExecutorSlot: 2, InputLength: 800, InputTokens: 200},
		7: {Status: types.StatusTimedOut, ExecutorSlot: 3, InputLength: 1200, ReservedCost: 700},
	}
}

func recordCountedRace(t *testing.T, book *Book) {
	t.Helper()
	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 5, Sent: true, InputTokens: 37, InputTokensSource: filters.InputTokensTokenizerExact},
		{Nonce: 6, Sent: true},
		{Nonce: 7, Sent: true, InputTokens: 290, InputTokensSource: filters.InputTokensTokenizerSampled},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
}

func sweep(t *testing.T, book *Book, latestNonce uint64, inferences map[uint64]*types.InferenceRecord) {
	t.Helper()
	if err := book.ObserveLatestNonce(testEscrow, latestNonce); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.ObserveInferences(testEscrow, inferences); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
}

// Test flow:
//  1. Record a race whose nonces carry an exact tokenizer count, no count, and a sampled count, and observe them finished, finished and timed out.
//  2. Read the slot money live, after a save and reload, after the chain seals them, and after one more save and reload.
//  3. Assert each time the counted nonces are estimated by their tokenizer count, marked tokenized, and the uncounted one by its bytes.
//  4. Assert no input count is kept once their costs are sealed.
func TestARacedNonceIsEstimatedByItsTokenizerCountAndTheRestByTheirBytes(t *testing.T) {
	live := newTestBook(t, 4)
	recordCountedRace(t, live)
	sweep(t, live, 7, countedRaceInferences())
	expected := map[string]SlotMoney{
		participantFor(0): {},
		participantFor(1): {Input: 100, EstimatedInput: 37, TokenizedInput: 37, CountedNonces: 1},
		participantFor(2): {Input: 200, EstimatedInput: filters.EstimatedPromptTokens(800), CountedNonces: 1},
		participantFor(3): {Reserved: 700, Refunded: 700, EstimatedError: 290, TokenizedError: 290},
	}
	store := openTestStore(t)
	totals := map[string]map[string]SlotMoney{"live": slotTotals(live.Query(QueryFilter{}))}
	restored := saveAndReload(t, live, store)
	totals["reloaded"] = slotTotals(restored.Query(QueryFilter{}))
	sweep(t, restored, 7, map[uint64]*types.InferenceRecord{})
	totals["sealed"] = slotTotals(restored.Query(QueryFilter{}))
	sealed := saveAndReload(t, restored, store)
	totals["sealed and reloaded"] = slotTotals(sealed.Query(QueryFilter{}))

	for name, money := range totals {
		for participant, slot := range money {
			if slot != expected[participant] {
				t.Fatalf("%s: %s holds %+v, want %+v", name, participant, slot, expected[participant])
			}
		}
	}
	if counts := sealed.Snapshot().Escrows[0].InputCounts; len(counts) != 0 {
		t.Fatalf("sealed escrow keeps input counts %+v, want none", counts)
	}
}

// Test flow:
//  1. Save a book with a finished inference and a sealed one, then strip the store to the shape before input counts: no tokenized money columns and no input count table.
//  2. Reopen the store, load and restore it, record a tokenizer-counted race on a new nonce and save it again.
//  3. Assert the stored nonces read back as byte estimates with nothing tokenized and no tokenizer share unknown, and the new nonce's count was saved beside them.
func TestAStoreWrittenBeforeInputCountsReadsAsByteEstimates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(): %v", err)
	}
	book := newTestBook(t, 4)
	for _, inferences := range []map[uint64]*types.InferenceRecord{
		{5: {Status: types.StatusFinished, InputLength: 400}, 6: {Status: types.StatusFinished, InputLength: 800}},
		{6: {Status: types.StatusFinished, InputLength: 800}},
	} {
		if err := book.ObserveInferences(testEscrow, inferences); err != nil {
			t.Fatalf("ObserveInferences(): %v", err)
		}
	}
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	stripInputCounts(t, path)

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() over the older store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restored := saveAndReload(t, book, reopened)
	if err := restored.RecordRace(testEscrow, []Attempt{{Nonce: 9, Sent: true, InputTokens: 11, InputTokensSource: filters.InputTokensTokenizerExact}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	reloaded := saveAndReload(t, restored, reopened)

	money := moneyOf(t, reloaded)
	if want := filters.EstimatedPromptTokens(400) + filters.EstimatedPromptTokens(800); money.EstimatedInput != want || money.TokenizedInput != 0 || money.CountedNonces != 2 {
		t.Fatalf("money = %+v, want %d estimated from bytes over 2 nonces, none tokenized", money, want)
	}
	if unknown := tokenizedUnknownSlots(reloaded); unknown != 0 {
		t.Fatalf("slots with an unknown tokenizer share = %d, want none for a store written before tokenizer counts", unknown)
	}
	if counts := reloaded.Snapshot().Escrows[0].InputCounts; len(counts) != 1 || counts[0] != (PersistedInputCount{Nonce: 9, Tokens: 11, Source: filters.InputTokensTokenizerExact}) {
		t.Fatalf("input counts = %+v, want nonce 9's 11 tokenizer tokens", counts)
	}
}

func stripInputCounts(t *testing.T, path string) {
	t.Helper()
	alterStore(t, path,
		`ALTER TABLE accounting_money DROP COLUMN tokenized_input`,
		`ALTER TABLE accounting_money DROP COLUMN tokenized_error`,
		`DROP TABLE accounting_input_counts`,
	)
}

func alterStore(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening the store as the older build left it: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// Test flow:
//  1. Save a book with a folded byte-estimated nonce, strip the store to the shape before input counts, and make every update of its money rows fail.
//  2. Reopen the store and assert the open fails and leaves neither tokenized column behind.
//  3. Let the updates through again, then reopen, load and restore the store.
//  4. Assert the slot reads as byte estimates with nothing tokenized and no tokenizer share unknown.
func TestAColumnWhoseStoredRowsCannotBeFilledIsNotAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(): %v", err)
	}
	book := newTestBook(t, 4)
	sweep(t, book, 6, map[uint64]*types.InferenceRecord{6: {Status: types.StatusFinished, InputLength: 800}})
	sweep(t, book, 6, map[uint64]*types.InferenceRecord{})
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	stripInputCounts(t, path)
	alterStore(t, path, `CREATE TRIGGER refuse_money_updates BEFORE UPDATE ON accounting_money BEGIN SELECT RAISE(ABORT, 'refused'); END`)

	if refused, err := OpenStore(path); err == nil {
		_ = refused.Close()
		t.Fatal("OpenStore() with the stored money rows refusing updates succeeded, want an error")
	}
	if columns := tokenizedColumnCount(t, path); columns != 0 {
		t.Fatalf("tokenized columns after the failed open = %d, want none so the next open adds and fills them", columns)
	}
	alterStore(t, path, `DROP TRIGGER refuse_money_updates`)
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() once updates are let through: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	snapshot, err := reopened.Load(t.Context())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	restored := NewBook(book.now)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("Restore(): %v", err)
	}

	if money := moneyOf(t, restored); money.EstimatedInput != filters.EstimatedPromptTokens(800) || money.TokenizedInput != 0 {
		t.Fatalf("money = %+v, want %d estimated from bytes, none tokenized", money, filters.EstimatedPromptTokens(800))
	}
	if unknown := tokenizedUnknownSlots(restored); unknown != 0 {
		t.Fatalf("slots with an unknown tokenizer share = %d, want none for a store written before tokenizer counts", unknown)
	}
}

func tokenizedColumnCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = db.Close() }()
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('accounting_money') WHERE name IN ('tokenized_input', 'tokenized_error')`).Scan(&columns); err != nil {
		t.Fatalf("inspecting the money columns: %v", err)
	}
	return columns
}

// Test flow:
//  1. Fold a nonce at its 37 tokenizer tokens and, on another slot, a ghost whose reserve timed out, then save the book.
//  2. Rewrite the escrow's money rows with the insert of a build that has no tokenized columns.
//  3. Load and restore the store, fold another nonce of the first slot at its 11 tokenizer tokens, then save and reload.
//  4. Assert both nonces still count in the estimated input and the ghost's reserve is kept, while only the first slot reports its tokenizer share as unknown rather than as zero.
func TestASaveByABuildWithoutTokenizedColumnsLeavesTheTokenizerShareUnknown(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordInputCount(testEscrow, 6, 37, filters.InputTokensTokenizerExact); err != nil {
		t.Fatalf("RecordInputCount(): %v", err)
	}
	if err := book.RecordGhost(testEscrow, 7, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	sweep(t, book, 7, map[uint64]*types.InferenceRecord{
		6: {Status: types.StatusFinished, InputLength: 4_000},
		7: {Status: types.StatusTimedOut, InputLength: 60, ReservedCost: 124},
	})
	sweep(t, book, 7, map[uint64]*types.InferenceRecord{})
	store := openTestStore(t)
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	rewriteMoneyWithoutTokenized(t, store)

	snapshot, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	restored := NewBook(book.now)
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("Restore(): %v", err)
	}
	if err := restored.RecordInputCount(testEscrow, 10, 11, filters.InputTokensTokenizerExact); err != nil {
		t.Fatalf("RecordInputCount(): %v", err)
	}
	sweep(t, restored, 10, map[uint64]*types.InferenceRecord{10: {Status: types.StatusFinished, InputLength: 400}})
	sweep(t, restored, 10, map[uint64]*types.InferenceRecord{})
	reloaded := saveAndReload(t, restored, store)

	if before := moneyOf(t, book); before.EstimatedInput != 37 || before.TokenizedInput != 37 || before.Reserved != 124 {
		t.Fatalf("money before the rewrite = %+v, want 37 estimated input, all tokenized, and the ghost's 124 reserved", before)
	}
	for _, check := range []struct {
		name string
		book *Book
	}{{"restored", restored}, {"reloaded", reloaded}} {
		money := moneyOf(t, check.book)
		if money.EstimatedInput != 37+11 || money.TokenizedInput != 0 || money.TokenizedError != 0 || money.Reserved != 124 {
			t.Fatalf("%s money = %+v, want 48 estimated input, no tokenizer share claimed, and the ghost's 124 reserved", check.name, money)
		}
		if unknown := tokenizedUnknownSlots(check.book); unknown != 1 {
			t.Fatalf("%s slots with an unknown tokenizer share = %d, want only the slot with estimates the rewrite touched", check.name, unknown)
		}
	}
}

func rewriteMoneyWithoutTokenized(t *testing.T, store *Store) {
	t.Helper()
	rows, err := store.db.QueryContext(t.Context(), `SELECT escrow_id, slot_id, reserved_cost, actual_cost, refunded_cost,
		estimated_input, estimated_error, max_tokens, input_tokens, counted_nonces, output_tokens FROM accounting_money`)
	if err != nil {
		t.Fatalf("reading the money rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var saved [][]any
	for rows.Next() {
		values := make([]any, 11)
		pointers := make([]any, len(values))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("scanning a money row: %v", err)
		}
		saved = append(saved, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the money rows: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), `DELETE FROM accounting_money`); err != nil {
		t.Fatalf("deleting the money rows: %v", err)
	}
	for _, values := range saved {
		if _, err := store.db.ExecContext(t.Context(), insertMoneyWithoutTokenized, values...); err != nil {
			t.Fatalf("rewriting a money row: %v", err)
		}
	}
}

func tokenizedUnknownSlots(book *Book) uint64 {
	var unknown uint64
	for _, record := range book.Query(QueryFilter{}) {
		unknown += record.TokenizedUnknownSlots
	}
	return unknown
}

// Test flow:
//  1. Observe a nonce finished before its race reported, then observe the escrow without it, as the chain seals it.
//  2. Record its race with a tokenizer count, and sweep the escrow once more.
//  3. Assert the sealed nonce keeps the byte estimate it was folded with and no input count is held for it.
func TestARaceReportedAfterItsNonceWasSealedKeepsNoCount(t *testing.T) {
	book := newTestBook(t, 4)
	for _, inferences := range []map[uint64]*types.InferenceRecord{{5: {Status: types.StatusFinished, InputLength: 400}}, {}} {
		sweep(t, book, 5, inferences)
	}

	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 5, Sent: true, InputTokens: 37, InputTokensSource: filters.InputTokensTokenizerExact}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	sweep(t, book, 5, map[uint64]*types.InferenceRecord{})

	money := moneyOf(t, book)
	if money.EstimatedInput != filters.EstimatedPromptTokens(400) || money.TokenizedInput != 0 {
		t.Fatalf("money = %+v, want the byte estimate the seal folded", money)
	}
	if counts := book.Snapshot().Escrows[0].InputCounts; len(counts) != 0 {
		t.Fatalf("input counts = %+v, want none for a sealed nonce", counts)
	}
}

// Test flow:
//  1. Record a tokenizer count for a nonce the escrow's sweeps have not seen yet.
//  2. Sweep the escrow at a watermark below that nonce, then at one that holds it finished, then once more with it sealed.
//  3. Assert the nonce was folded with its tokenizer count and no input count is left behind.
func TestACountRecordedBeforeTheSweepSeesItsNonceOutlivesThatSweep(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 6, Sent: true, InputTokens: 37, InputTokensSource: filters.InputTokensTokenizerExact}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	finished := &types.InferenceRecord{Status: types.StatusFinished, InputLength: 400}
	sweep(t, book, 5, map[uint64]*types.InferenceRecord{5: finished})
	sweep(t, book, 6, map[uint64]*types.InferenceRecord{5: finished, 6: {Status: types.StatusFinished, InputLength: 4_000}})
	sweep(t, book, 6, map[uint64]*types.InferenceRecord{})

	money := moneyOf(t, book)
	if money.EstimatedInput != filters.EstimatedPromptTokens(400)+37 || money.TokenizedInput != 37 {
		t.Fatalf("money = %+v, want nonce 6 folded at its 37 tokenizer tokens", money)
	}
	if counts := book.Snapshot().Escrows[0].InputCounts; len(counts) != 0 {
		t.Fatalf("input counts = %+v, want none once both nonces are sealed", counts)
	}
}

// Test flow:
//  1. Start a sweep that reads a watermark holding a just-assigned nonce, before it observes the nonce's cost.
//  2. File the nonce's tokenizer count at assignment in between, then let the sweep observe the nonce pending and a later sweep observe it sealed.
//  3. Assert the nonce was folded with its tokenizer count.
func TestACountFiledWhileASweepIsBetweenItsWatermarkAndItsCostsIsKept(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 6); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordInputCount(testEscrow, 6, 37, filters.InputTokensTokenizerExact); err != nil {
		t.Fatalf("RecordInputCount(): %v", err)
	}
	if err := book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{6: {Status: types.StatusPending, InputLength: 4_000}}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
	sweep(t, book, 6, map[uint64]*types.InferenceRecord{6: {Status: types.StatusFinished, InputLength: 4_000}})
	sweep(t, book, 6, map[uint64]*types.InferenceRecord{})

	if money := moneyOf(t, book); money.EstimatedInput != 37 || money.TokenizedInput != 37 {
		t.Fatalf("money = %+v, want nonce 6 folded at its 37 tokenizer tokens", money)
	}
}

// Test flow:
//  1. Record a race whose nonce carries a tokenizer count and save the book.
//  2. Sweep the escrow at a watermark holding that nonce with nothing live, so the sweep changes nothing but the count it drops.
//  3. Save and reload, and assert the dropped count left the store too.
func TestACountDroppedByASweepLeavesTheStore(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 5, Sent: true, InputTokens: 37, InputTokensSource: filters.InputTokensTokenizerExact}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	store := openTestStore(t)
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	sweep(t, book, 5, map[uint64]*types.InferenceRecord{})
	if counts := book.Snapshot().Escrows[0].InputCounts; len(counts) != 0 {
		t.Fatalf("input counts in the book = %+v, want none once the sweep passed nonce 5", counts)
	}

	reloaded := saveAndReload(t, book, store)

	if counts := reloaded.Snapshot().Escrows[0].InputCounts; len(counts) != 0 {
		t.Fatalf("input counts in the store = %+v, want none once the sweep passed nonce 5", counts)
	}
}

// Test flow:
//  1. Record a tokenizer count for each of 50,000 nonces of one escrow.
//  2. Sweep the escrow at a watermark holding them all with nothing live, so every count is dropped, and retire it.
//  3. Assert the retired escrow keeps less than 256 KiB of heap after a collection.
func TestARetiredEscrowFreesTheCountsItDropped(t *testing.T) {
	before := heapInUse()
	book := newTestBook(t, 4)
	for nonce := uint64(1); nonce <= retiredCountedNonces; nonce++ {
		if err := book.RecordInputCount(testEscrow, nonce, 37, filters.InputTokensTokenizerExact); err != nil {
			t.Fatalf("RecordInputCount(): %v", err)
		}
	}
	sweep(t, book, retiredCountedNonces, map[uint64]*types.InferenceRecord{})
	book.RetireEscrow(testEscrow)

	retained := int64(heapInUse()) - int64(before)
	runtime.KeepAlive(book)

	if retained >= retiredCountsHeapAllowance {
		t.Fatalf("retired escrow keeps %d bytes of heap, want under %d once its counts are dropped", retained, retiredCountsHeapAllowance)
	}
}
