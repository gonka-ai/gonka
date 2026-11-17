package accounting

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

const (
	lockTestGroupSize      = 4
	lockTestRetired        = "escrow-3"
	lockTestRounds         = 120
	lockTestFeePerNonce    = 10
	lockTestNoncesPerRound = 4
)

func steppingClock() func() time.Time {
	var mu sync.Mutex
	current := time.Unix(1700000000, 0).UTC()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(time.Second)
		return current
	}
}

func storedRows(t *testing.T, store *Store) []string {
	t.Helper()
	dump := make([]string, 0, len(clearedTables))
	for _, table := range clearedTables {
		dump = append(dump, storedTableRows(t, store, table)...)
	}
	slices.Sort(dump)
	return dump
}

func storedTableRows(t *testing.T, store *Store, table string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(t.Context(), "SELECT * FROM "+table)
	if err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("reading the columns of %s: %v", table, err)
	}
	var dump []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("scanning %s: %v", table, err)
		}
		dump = append(dump, table+" "+fmt.Sprint(values...))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the rows of %s: %v", table, err)
	}
	return dump
}

func saveEveryEscrow(t *testing.T, store *Store, book *Book) {
	t.Helper()
	book.requeueUnsaved(unsavedChanges{escrowIDs: book.EscrowIDs()})
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save(every escrow) = %v, want nil", err)
	}
}

func liveInferences(from, through uint64, status types.InferenceStatus) map[uint64]*types.InferenceRecord {
	inferences := make(map[uint64]*types.InferenceRecord)
	for nonce := from; nonce <= through; nonce++ {
		inferences[nonce] = &types.InferenceRecord{
			Status: status, ExecutorSlot: uint32(nonce % lockTestGroupSize), ReservedCost: 1200, ActualCost: 900 + nonce,
			InputTokens: 400, OutputTokens: 250,
		}
	}
	return inferences
}

func sweepEscrow(book *Book, escrowID string, latest, cost uint64, inferences map[uint64]*types.InferenceRecord) {
	_ = book.ObserveLatestNonce(escrowID, latest)
	_ = book.ObserveCharging(escrowID, lockTestFeePerNonce, 0)
	for slotID := range uint32(lockTestGroupSize) {
		_ = book.ObserveHostStats(escrowID, slotID, types.HostStats{Missed: 1, Cost: cost + uint64(slotID)})
	}
	_ = book.ObserveInferences(escrowID, inferences)
}

func recordWonRace(book *Book, escrowID string, nonce uint64) {
	_ = book.RecordAssigned(escrowID, nonce, fmt.Sprintf("request-%d", nonce))
	_ = book.RecordRace(escrowID, []Attempt{{
		Nonce: nonce, RequestID: fmt.Sprintf("request-%d", nonce), Sent: true, Acknowledged: true, OutputTokens: 250,
		Usage: UsageWinner, Terminal: engine.TerminalWon.String(), Phase: PhaseNormal,
		InputTokens: 37 + nonce, InputTokensSource: filters.InputTokensTokenizerExact,
		FirstContentDelay: time.Duration(nonce) * 100 * time.Millisecond,
	}})
}

func ledgerScript() []func(*Book) {
	return []func(*Book){
		func(book *Book) {
			for _, escrowID := range []string{testEscrow, otherEscrow, lockTestRetired} {
				sweepEscrow(book, escrowID, 0, 900, nil)
			}
		},
		func(book *Book) {
			for nonce := uint64(1); nonce <= 6; nonce++ {
				recordWonRace(book, testEscrow, nonce)
			}
			_ = book.RecordAssigned(testEscrow, 7, "request-7")
			_ = book.RecordTimeout(testEscrow, 7, engine.TimeoutKindExecution, engine.TimeoutActionCompleted, engine.TimeoutReasonNotApplied)
			_ = book.RecordGhost(testEscrow, 8, scheduler.GhostReasonWindowFull)
			sweepEscrow(book, testEscrow, 8, 900, liveInferences(1, 8, types.StatusFinished))
			recordWonRace(book, lockTestRetired, 2)
			sweepEscrow(book, lockTestRetired, 2, 900, liveInferences(1, 2, types.StatusStarted))
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 8, 900, liveInferences(1, 8, types.StatusFinished))
			sweepEscrow(book, otherEscrow, 0, 900, nil)
			sweepEscrow(book, lockTestRetired, 2, 900, liveInferences(1, 2, types.StatusStarted))
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 8, 900, liveInferences(5, 8, types.StatusFinished))
			recordWonRace(book, otherEscrow, 1)
			sweepEscrow(book, otherEscrow, 1, 905, liveInferences(1, 1, types.StatusChallenged))
		},
		func(book *Book) {
			_ = book.RecordAppliedTimeout(testEscrow, 7)
			_ = book.RecordValidation(testEscrow, 1)
			_ = book.RecordInvalidVerdict(otherEscrow, 1)
			sweepEscrow(book, testEscrow, 8, 950, liveInferences(5, 8, types.StatusFinished))
		},
		func(book *Book) {
			sweepEscrow(book, lockTestRetired, 2, 900, liveInferences(1, 2, types.StatusFinished))
			sweepEscrow(book, lockTestRetired, 2, 900, nil)
		},
		func(book *Book) {
			sweepEscrow(book, lockTestRetired, 2, 900, nil)
		},
		func(book *Book) {
			book.RetireEscrow(lockTestRetired)
		},
		func(book *Book) {
			book.RetireEscrow(lockTestRetired)
			sweepEscrow(book, testEscrow, 8, 950, liveInferences(5, 8, types.StatusFinished))
			sweepEscrow(book, otherEscrow, 1, 905, liveInferences(1, 1, types.StatusChallenged))
		},
		func(book *Book) {
			recordWonRace(book, lockTestRetired, 3)
			_ = book.RecordServiceNonce(otherEscrow, 2, ServiceFinalize)
			_ = book.ObserveCharging(otherEscrow, lockTestFeePerNonce, 2)
			sweepEscrow(book, testEscrow, 9, 950, nil)
		},
		func(book *Book) {
			book.RetireEscrow(lockTestRetired)
			sweepEscrow(book, testEscrow, 9, 950, nil)
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 9, 960, nil)
		},
		func(book *Book) {
			_ = book.ObserveCharging(otherEscrow, lockTestFeePerNonce+1, 2)
		},
		func(book *Book) {
			_ = book.ObserveLatestNonce(otherEscrow, 3)
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 9, 960, liveInferences(9, 9, types.StatusStarted))
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 9, 960, liveInferences(9, 9, types.StatusChallenged))
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 9, 960, nil)
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 10, 960, map[uint64]*types.InferenceRecord{10: {Status: types.StatusFinished, ExecutorSlot: 2, ActualCost: 700}})
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 10, 960, map[uint64]*types.InferenceRecord{10: {Status: types.StatusFinished, ExecutorSlot: 2, ActualCost: 800}})
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 10, 960, map[uint64]*types.InferenceRecord{10: {Status: types.StatusChallenged, ExecutorSlot: 2, ActualCost: 800}})
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 10, 960, map[uint64]*types.InferenceRecord{10: {Status: types.StatusChallenged, ExecutorSlot: 3, ActualCost: 800}})
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 11, 960, liveInferences(11, 11, types.StatusFinished))
		},
		func(book *Book) {
			recordWonRace(book, testEscrow, 11)
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 11, 960, liveInferences(11, 11, types.StatusFinished))
		},
		func(book *Book) {
			_ = book.ObserveLatestNonce(testEscrow, 20)
		},
		func(book *Book) {
			_ = book.RecordInputCount(testEscrow, 20, 55, filters.InputTokensTokenizerExact)
			_ = book.RecordInputCount(testEscrow, 21, 56, filters.InputTokensTokenizerExact)
			_ = book.RecordInputCount(otherEscrow, 4, 57, filters.InputTokensTokenizerExact)
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 20, 960, liveInferences(11, 11, types.StatusFinished))
		},
		func(book *Book) {
			_ = book.RecordGhost(otherEscrow, 4, scheduler.GhostReasonWindowFull)
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 21, 960, liveInferences(11, 11, types.StatusFinished))
		},
		func(book *Book) {
			sweepEscrow(book, testEscrow, 21, 960, liveInferences(11, 11, types.StatusFinished))
		},
	}
}

func newScriptBook(t *testing.T) *Book {
	t.Helper()
	book := NewBook(steppingClock())
	for _, escrowID := range []string{testEscrow, otherEscrow, lockTestRetired} {
		openTestEscrow(t, book, escrowID, testEpoch, lockTestGroupSize)
	}
	return book
}

// Test flow:
//  1. Drive two books through the same script of facts carrying tokenizer counts and first-content delays, sweeps, sweeps that change nothing, a retirement alone after a sweep that changed nothing, late facts, counts filed above the watermark, a sweep whose only change is dropping a count, and a ghost that drops one.
//  2. After each step, save the first book as it marks itself and the second with every escrow marked, each into its own store.
//  3. Assert both stores hold the same rows after every step, the stored updated_at included.
func TestASaveOfTheEscrowsMarkedChangedStoresWhatAWholeWriteStores(t *testing.T) {
	incremental, whole := newScriptBook(t), newScriptBook(t)
	incrementalStore, wholeStore := openTestStore(t), openTestStore(t)

	for step, apply := range ledgerScript() {
		apply(incremental)
		apply(whole)
		if err := incrementalStore.Save(t.Context(), incremental); err != nil {
			t.Fatalf("step %d: Save() = %v, want nil", step, err)
		}
		saveEveryEscrow(t, wholeStore, whole)

		got, want := storedRows(t, incrementalStore), storedRows(t, wholeStore)
		if !slices.Equal(got, want) {
			t.Fatalf("step %d: stored rows differ from a whole write\n got: %v\nwant: %v", step, got, want)
		}
	}
}

// Test flow:
//  1. Open a live escrow and a retired one, sweep the live one, and save.
//  2. Overwrite both stored watermarks behind the book's back.
//  3. Sweep the live escrow with the values it already holds, retire the retired one again, and save.
//  4. Assert the book's updated_at still moved on both calls.
//  5. Assert neither escrow's row was rewritten, while the stored updated_at moved to the book's.
func TestASweepThatChangesNothingLeavesTheStoredEscrowsAlone(t *testing.T) {
	book := NewBook(steppingClock())
	openTestEscrow(t, book, testEscrow, testEpoch, lockTestGroupSize)
	openTestEscrow(t, book, lockTestRetired, testEpoch, lockTestGroupSize)
	sweepEscrow(book, testEscrow, 8, 900, liveInferences(1, 8, types.StatusFinished))
	book.RetireEscrow(lockTestRetired)
	store := openTestStore(t)
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE accounting_escrows SET latest_nonce = 999`); err != nil {
		t.Fatalf("marking the stored rows: %v", err)
	}

	beforeSweep := book.Snapshot().UpdatedAt
	sweepEscrow(book, testEscrow, 8, 900, liveInferences(1, 8, types.StatusFinished))
	beforeRetirement := book.Snapshot().UpdatedAt
	book.RetireEscrow(lockTestRetired)
	afterRetirement := book.Snapshot().UpdatedAt
	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}

	if !beforeRetirement.After(beforeSweep) || !afterRetirement.After(beforeRetirement) {
		t.Fatalf("updated_at went %v, %v, %v over a sweep and a retirement that changed nothing, want it to move on each", beforeSweep, beforeRetirement, afterRetirement)
	}
	for _, escrowID := range []string{testEscrow, lockTestRetired} {
		if got := storedLatestNonce(t, store, escrowID); got != 999 {
			t.Fatalf("stored latest_nonce of %s = %d after a sweep that changed nothing, want 999", escrowID, got)
		}
	}
	snapshot, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if want := book.Snapshot().UpdatedAt; !snapshot.UpdatedAt.Equal(want) {
		t.Fatalf("stored updated_at = %v, want the book's %v", snapshot.UpdatedAt, want)
	}
}

// Test flow:
//  1. Open three escrows over a store.
//  2. Run sweeps, dispatcher facts, read routes and saves on their own goroutines at once, retiring one escrow halfway through the sweeps.
//  3. Save once more after they all return.
//  4. Assert the store holds exactly the rows a whole write of the final book holds.
func TestConcurrentSavesReadsAndSweepsStoreWhatAWholeWriteStores(t *testing.T) {
	book := NewBook(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	for _, escrowID := range []string{testEscrow, otherEscrow, lockTestRetired} {
		openTestEscrow(t, book, escrowID, testEpoch, lockTestGroupSize)
	}
	store := openTestStore(t)
	var group sync.WaitGroup
	group.Go(func() {
		for round := uint64(1); round <= lockTestRounds; round++ {
			latest := round * lockTestNoncesPerRound
			for _, escrowID := range []string{testEscrow, otherEscrow} {
				sweepEscrow(book, escrowID, latest, 900+round, liveInferences(latest-lockTestNoncesPerRound+1, latest, types.StatusFinished))
			}
			if round == lockTestRounds/2 {
				sweepEscrow(book, lockTestRetired, latest, 900, nil)
				book.RetireEscrow(lockTestRetired)
			}
		}
	})
	group.Go(func() {
		for nonce := uint64(1); nonce <= lockTestRounds*lockTestNoncesPerRound; nonce++ {
			recordWonRace(book, testEscrow, nonce)
			recordWonRace(book, otherEscrow, nonce)
			if nonce%lockTestNoncesPerRound == 0 {
				_ = book.RecordTimeout(otherEscrow, nonce, engine.TimeoutKindExecution, engine.TimeoutActionCompleted, engine.TimeoutReasonNotApplied)
			}
		}
	})
	group.Go(func() {
		for range lockTestRounds {
			_ = book.Query(QueryFilter{EpochIndex: testEpoch})
			_ = book.Epochs(QueryFilter{})
		}
	})
	saveErrors := make(chan error, lockTestRounds)
	group.Go(func() {
		for range lockTestRounds {
			saveErrors <- store.Save(t.Context(), book)
		}
		close(saveErrors)
	})
	group.Wait()
	for err := range saveErrors {
		if err != nil {
			t.Fatalf("concurrent Save() = %v, want nil", err)
		}
	}

	if err := store.Save(t.Context(), book); err != nil {
		t.Fatalf("Save() = %v, want nil", err)
	}

	whole := openTestStore(t)
	saveEveryEscrow(t, whole, book)
	if got, want := storedRows(t, store), storedRows(t, whole); !slices.Equal(got, want) {
		t.Fatalf("stored rows after concurrent saves differ from a whole write\n got: %v\nwant: %v", got, want)
	}
}
