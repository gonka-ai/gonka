package accounting

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/types"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "accounting.db"))
	if err != nil {
		t.Fatalf("OpenStore(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// saveAndReload writes the book and reads it into a fresh one, which is what a restart does.
func saveAndReload(t *testing.T, book *Book, store *Store) *Book {
	t.Helper()
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	restored := NewBook(func() time.Time { return time.Unix(0, 0).UTC() })
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("Restore(): %v", err)
	}
	return restored
}

// Test flow:
//  1. Observe the latest nonce, record a ghost, and observe host stats reporting misses.
//  2. Save and reload the book.
//  3. Assert the restored records match the originals in participant, assigned count, ghost count and chain-missed tally.
func TestCountersSurviveARestart(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 12); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordGhost(testEscrow, 5, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	if err := book.ObserveHostStats(testEscrow, 1, types.HostStats{Missed: 2}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}

	restored := saveAndReload(t, book, openTestStore(t))

	before, after := book.Query(QueryFilter{}), restored.Query(QueryFilter{})
	if len(before) != len(after) {
		t.Fatalf("restored %d participants, want %d", len(after), len(before))
	}
	for index, want := range before {
		got := after[index]
		if got.Participant != want.Participant || got.Assigned != want.Assigned {
			t.Fatalf("restored %+v, want participant %s with %d assigned", got, want.Participant, want.Assigned)
		}
		if got.Dispositions[DispositionGhost] != want.Dispositions[DispositionGhost] {
			t.Fatalf("restored %d ghosts for %s, want %d",
				got.Dispositions[DispositionGhost], want.Participant, want.Dispositions[DispositionGhost])
		}
		if got.ChainMissed != want.ChainMissed {
			t.Fatalf("restored %d chain misses for %s, want %d", got.ChainMissed, want.Participant, want.ChainMissed)
		}
	}
}

// Test flow:
//  1. Observe the latest nonce and record an unfinished race on nonce 6, leaving it pending.
//  2. Save and reload the book.
//  3. Assert the restored slot has no pending count, and its disposition is `DispositionUnfinishedRefused` since no receipt ever arrived.
//  4. Assert every counter of the nonce's participant carries `engine.TimeoutActionAbandoned`.
func TestANonceLeftPendingByARestartIsNamedRatherThanLost(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 12); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 6, Sent: true}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	if pending := unclassifiedOfSlot(t, book, slotOfNonce(6, 4)); pending != 1 {
		t.Fatalf("pending = %d before the restart, want the unfinished nonce", pending)
	}

	restored := saveAndReload(t, book, openTestStore(t))

	if pending := unclassifiedOfSlot(t, restored, slotOfNonce(6, 4)); pending != 0 {
		t.Fatalf("pending = %d after the restart, want the nonce classified", pending)
	}
	assertDisposition(t, restored, 6, 4, DispositionUnfinishedRefused)
	for _, record := range restored.Query(QueryFilter{}) {
		if record.Participant != participantFor(2) {
			continue
		}
		for _, counter := range record.Counters {
			if counter.TimeoutAction != engine.TimeoutActionAbandoned {
				t.Fatalf("timeout action = %q, want the abandonment named", counter.TimeoutAction)
			}
		}
	}
}

// Test flow:
//  1. Observe the latest nonce, record a race on nonce 6, and record its timeout as `engine.TimeoutActionStarted`, a vote still posting.
//  2. Save and reload the book.
//  3. Assert no counter still reads `engine.TimeoutActionStarted` after the restart.
//  4. Assert at least one counter now reads `engine.TimeoutActionAbandoned`.
func TestAVoteStillPostingAtARestartIsNamedAbandoned(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 12); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 6, Sent: true}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	if err := book.RecordTimeout(testEscrow, 6, engine.TimeoutKindRefused, engine.TimeoutActionStarted, engine.TimeoutReasonNone); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}

	restored := saveAndReload(t, book, openTestStore(t))

	abandoned := 0
	for _, record := range restored.Query(QueryFilter{}) {
		for _, counter := range record.Counters {
			if counter.TimeoutAction == engine.TimeoutActionStarted {
				t.Fatalf("counter %+v still reads started after the restart", counter)
			}
			if counter.TimeoutAction == engine.TimeoutActionAbandoned {
				abandoned++
			}
		}
	}
	if abandoned == 0 {
		t.Fatal("the vote the restart cut off was not named abandoned")
	}
}

// Test flow:
//  1. Load a snapshot from a freshly opened, never-written store.
//  2. Restore it into a new book.
//  3. Assert the book holds no escrow ids.
func TestAnEmptyStoreStartsAnEmptyLedger(t *testing.T) {
	snapshot, err := openTestStore(t).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() on a first run: %v", err)
	}
	book := NewBook(nil)
	if err := book.Restore(snapshot); err != nil {
		t.Fatalf("Restore(): %v", err)
	}
	if escrows := book.EscrowIDs(); len(escrows) != 0 {
		t.Fatalf("EscrowIDs() = %v, want none", escrows)
	}
}

// Test flow:
//  1. Write a schema-version row of "99" directly into the store's meta table.
//  2. Attempt to load the store.
//  3. Assert the load returns an error rather than accepting a ledger from another schema.
func TestAStoreFromAnotherSchemaIsRefused(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(
		`INSERT INTO accounting_meta (key, value) VALUES ('schema_version', '99')`); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("Load() accepted a ledger from another schema")
	}
}

// Test flow:
//  1. Save a book holding one ghost record.
//  2. Build a second, broken book whose escrow has two slots claiming the same slot id, which collides on the store's primary key.
//  3. Save the broken book and assert the save fails.
//  4. Load the store and assert it still holds the previous ledger's one escrow and one counter, untouched by the failed write.
func TestAFailedWriteLeavesThePreviousLedgerInPlace(t *testing.T) {
	store := openTestStore(t)
	book := newTestBook(t, 4)
	if err := book.RecordGhost(testEscrow, 5, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	broken := NewBook(func() time.Time { return time.Unix(0, 0).UTC() })
	if err := broken.OpenEscrow(EscrowMetadata{
		EscrowID: testEscrow, Model: testModel, CreationEpoch: testEpoch,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: participantFor(0)},
			{SlotID: 0, ValidatorAddress: participantFor(1)},
		},
	}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}
	if err := store.Save(context.Background(), broken); err == nil {
		t.Fatal("Save() reported success writing two slots with one id")
	}

	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() after the failed write: %v", err)
	}
	if len(snapshot.Escrows) != 1 || len(snapshot.Escrows[0].Counters) != 1 {
		t.Fatalf("ledger after the failed write = %+v, want the previous one whole", snapshot.Escrows)
	}
}

// Test flow:
//  1. Record a losing race on nonce 6, then time it out with a collection error, and assert it lands in `DispositionUnfinishedExecution`.
//  2. Read the unfinished nonces and assert nonce 6 is the only one.
//  3. Mark it finished.
//  4. Assert its disposition moves to `DispositionFinishedUnused` and the unfinished list becomes empty.
func TestALateFinishLiftsANonceOutOfTheUnfinishedBucket(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 6, Sent: true, Usage: UsageLoser}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	if err := book.RecordTimeout(testEscrow, 6, "execution", "failed", "timeout_collection_error"); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}
	assertDisposition(t, book, 6, 4, DispositionUnfinishedExecution)

	unfinished := book.UnfinishedNonces(testEscrow)
	if len(unfinished) != 1 || unfinished[0] != 6 {
		t.Fatalf("UnfinishedNonces() = %v, want the one nonce worth re-asking about", unfinished)
	}
	if err := book.MarkFinished(testEscrow, unfinished); err != nil {
		t.Fatalf("MarkFinished(): %v", err)
	}

	assertDisposition(t, book, 6, 4, DispositionFinishedUnused)
	if left := book.UnfinishedNonces(testEscrow); len(left) != 0 {
		t.Fatalf("UnfinishedNonces() = %v, want none once the finish landed", left)
	}
}

// Test flow:
//  1. Record a ghost on nonce 5, a finished winning race on nonce 9, and a losing unfinished race on nonce 6, then time nonce 6 out with a collection error.
//  2. Read the snapshot's stored nonces for the escrow.
//  3. Assert only nonce 6, the one that can still move, is written down.
func TestOnlyRevisableNoncesAreWrittenDown(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordGhost(testEscrow, 5, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 9, Sent: true, Usage: UsageWinner},
		{Nonce: 6, Sent: true, Usage: UsageLoser},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 9)
	if err := book.RecordTimeout(testEscrow, 6, "execution", "failed", "timeout_collection_error"); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}

	stored := book.Snapshot().Escrows[0].Nonces
	if len(stored) != 1 || stored[0].Nonce != 6 {
		t.Fatalf("stored nonces = %+v, want only the unfinished one", stored)
	}
}

// Test flow:
//  1. Record a losing unfinished race on nonce 6, then time it out with a collection error.
//  2. Save and reload the book, then mark the restored book's unfinished nonces finished.
//  3. Assert the disposition is `DispositionFinishedUnused`, moved out of its bucket rather than counted twice.
func TestARestoredUnfinishedNonceIsLiftedRatherThanCountedTwice(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 6, Sent: true, Usage: UsageLoser}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	if err := book.RecordTimeout(testEscrow, 6, "execution", "failed", "timeout_collection_error"); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}

	restored := saveAndReload(t, book, openTestStore(t))
	if err := restored.MarkFinished(testEscrow, restored.UnfinishedNonces(testEscrow)); err != nil {
		t.Fatalf("MarkFinished(): %v", err)
	}

	assertDisposition(t, restored, 6, 4, DispositionFinishedUnused)
}

// Test flow:
//  1. Build a service with retention of 2 epochs and a current epoch of 10.
//  2. Open and retire an escrow in each of epochs 7, 8, 9 and 10.
//  3. Run the service's prune.
//  4. Assert only the escrows of epochs 10, 9 and 8 remain, the current epoch and the two before it.
func TestRetentionKeepsTheCurrentEpochAndTheOnesBeforeIt(t *testing.T) {
	const currentEpoch = 10
	service, err := NewService(Settings{
		RetentionEpochs: 2,
		CurrentEpoch:    func(context.Context) (uint64, error) { return currentEpoch, nil },
		Now:             func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	for _, epoch := range []uint64{7, 8, 9, 10} {
		escrowID := "escrow-" + strconv.FormatUint(epoch, 10)
		if err := service.Book.OpenEscrow(EscrowMetadata{
			EscrowID: escrowID, Model: testModel, CreationEpoch: epoch,
			Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: participantFor(0)}},
		}); err != nil {
			t.Fatalf("OpenEscrow(): %v", err)
		}
		service.Book.RetireEscrow(escrowID)
	}

	service.prune(context.Background())

	kept := service.Book.EscrowIDs()
	want := []string{"escrow-10", "escrow-8", "escrow-9"}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v, want %v — the current epoch and the two before it", kept, want)
	}
}

// Test flow:
//  1. Build a service with retention of 2 epochs and a current epoch of 10.
//  2. Open a live (not retired) escrow created back in epoch 1.
//  3. Run the service's prune.
//  4. Assert the escrow is still kept, since it is still being served however old it is.
func TestRetentionNeverDropsAnEscrowStillBeingServed(t *testing.T) {
	service, err := NewService(Settings{
		RetentionEpochs: 2,
		CurrentEpoch:    func(context.Context) (uint64, error) { return 10, nil },
		Now:             func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	if err := service.Book.OpenEscrow(EscrowMetadata{
		EscrowID: "escrow-ancient", Model: testModel, CreationEpoch: 1,
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: participantFor(0)}},
	}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}

	service.prune(context.Background())

	if kept := service.Book.EscrowIDs(); len(kept) != 1 {
		t.Fatalf("kept %v, want the live escrow untouched", kept)
	}
}

// Test flow:
//  1. Record a race of two attempts on nonces 1 and 2, differing only by phase, terminal and a slow-chunk flag.
//  2. Save the book, then save and reload it again.
//  3. Assert the restored counters still hold both `PhaseNormal` and `PhasePoC` buckets.
func TestCountersDifferingOnlyByARaceFactBothPersist(t *testing.T) {
	store := openTestStore(t)
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 2); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 1, Sent: true, Acknowledged: true, Usage: UsageWinner, Terminal: "won", Phase: PhaseNormal},
		{Nonce: 2, Sent: true, Acknowledged: true, Usage: UsageWinner, Terminal: "won", Phase: PhasePoC, SlowChunk: true},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 1, 2)

	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	restored := saveAndReload(t, book, store)
	phases := make(map[Phase]bool)
	for _, counter := range restored.Query(QueryFilter{})[0].Counters {
		phases[counter.Phase] = true
	}
	if !phases[PhaseNormal] || !phases[PhasePoC] {
		t.Fatalf("reloaded phases %v, want both buckets to survive the round trip", phases)
	}
}

// Test flow:
//  1. Seed a database with an old-shaped `accounting_meta` and `accounting_counters` table at schema version 1.
//  2. Open a store over that file and save a book with one race attempt.
//  3. Assert the save succeeds, so the old tables were discarded rather than left in place.
func TestAStoreFromAnEarlierSchemaIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	stale, err := sql.Open("sqlite", path+connectionPragmas)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	if _, err := stale.Exec(`
		CREATE TABLE accounting_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO accounting_meta (key, value) VALUES ('schema_version', '1');
		CREATE TABLE accounting_counters (
			escrow_id TEXT NOT NULL, slot_id INTEGER NOT NULL, disposition TEXT NOT NULL,
			ghost_reason TEXT NOT NULL, timeout_kind TEXT NOT NULL, timeout_action TEXT NOT NULL,
			timeout_reason TEXT NOT NULL, count INTEGER NOT NULL,
			PRIMARY KEY (escrow_id, slot_id, disposition, ghost_reason, timeout_kind, timeout_action, timeout_reason));`); err != nil {
		t.Fatalf("seeding an older store: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() over an older schema: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	book := newTestBook(t, 1)
	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 1, Sent: true, Acknowledged: true, Usage: UsageWinner, Terminal: "won"},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 1)
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save() over a discarded older schema: %v", err)
	}
}

// Test flow:
//  1. Observe the latest nonce and record a race of two attempts, each field set to a distinguishable value: terminal, phase, slow receipt, slow chunk, clock drift.
//  2. Take the book's own snapshot, then save and load the store's snapshot.
//  3. Assert the escrow counts match between the two snapshots.
//  4. Assert each escrow's counters and nonces are equal between what was written and what was read back.
func TestEveryPersistedFieldSurvivesTheRoundTrip(t *testing.T) {
	store := openTestStore(t)
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 2); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordRace(testEscrow, []Attempt{
		{
			Nonce: 1, Sent: true, Acknowledged: true, Usage: UsageLoser,
			Terminal: "lost", Phase: PhasePoC, SlowReceipt: true, SlowChunk: true, ClockDrifted: true,
		},
		{
			Nonce: 2, Sent: true, Acknowledged: true, Usage: UsageWinner,
			Terminal: "stalled", Phase: PhaseNormal, SlowChunk: true,
		},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 1)

	written := book.Snapshot()
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	read, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if len(read.Escrows) != len(written.Escrows) {
		t.Fatalf("read %d escrows, wrote %d", len(read.Escrows), len(written.Escrows))
	}
	for index := range written.Escrows {
		if !slices.Equal(read.Escrows[index].Counters, written.Escrows[index].Counters) {
			t.Fatalf("counters differ:\n wrote %+v\n read  %+v",
				written.Escrows[index].Counters, read.Escrows[index].Counters)
		}
		if !slices.Equal(read.Escrows[index].Nonces, written.Escrows[index].Nonces) {
			t.Fatalf("nonces differ:\n wrote %+v\n read  %+v",
				written.Escrows[index].Nonces, read.Escrows[index].Nonces)
		}
	}
}

// Test flow:
//  1. Seed a database whose `accounting_meta` already carries the current `SchemaVersion` but whose `accounting_counters` table is the old shape.
//  2. Open a store over that file and save a book with one race attempt.
//  3. Assert the save succeeds, so a shape mismatch is caught even when the version number agrees.
func TestAStoreWhoseVersionAgreesButShapeDoesNotIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	stale, err := sql.Open("sqlite", path+connectionPragmas)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	if _, err := stale.Exec(`
		CREATE TABLE accounting_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO accounting_meta (key, value) VALUES ('schema_version', '` + strconv.Itoa(SchemaVersion) + `');
		CREATE TABLE accounting_counters (
			escrow_id TEXT NOT NULL, slot_id INTEGER NOT NULL, disposition TEXT NOT NULL,
			ghost_reason TEXT NOT NULL, timeout_kind TEXT NOT NULL, timeout_action TEXT NOT NULL,
			timeout_reason TEXT NOT NULL, count INTEGER NOT NULL,
			PRIMARY KEY (escrow_id, slot_id, disposition, ghost_reason, timeout_kind, timeout_action, timeout_reason));`); err != nil {
		t.Fatalf("seeding a store of the right version and the wrong shape: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	book := newTestBook(t, 1)
	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 1, Sent: true, Acknowledged: true, Usage: UsageWinner, Terminal: "won"},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 1)
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save() over a store whose shape this build cannot write: %v", err)
	}
}

// Test flow:
//  1. For each table case (an empty version table, and no version table at all), seed a database with the old-shaped `accounting_counters` table.
//  2. Open a store over that file and save a book with one race attempt.
//  3. Assert the save succeeds, so a store with no readable version is discarded rather than trusted.
func TestAStoreWhoseVersionCannotBeReadIsDiscarded(t *testing.T) {
	staleCounters := `
		CREATE TABLE accounting_counters (
			escrow_id TEXT NOT NULL, slot_id INTEGER NOT NULL, disposition TEXT NOT NULL,
			ghost_reason TEXT NOT NULL, timeout_kind TEXT NOT NULL, timeout_action TEXT NOT NULL,
			timeout_reason TEXT NOT NULL, count INTEGER NOT NULL,
			PRIMARY KEY (escrow_id, slot_id, disposition, ghost_reason, timeout_kind, timeout_action, timeout_reason));`

	cases := []struct {
		name string
		seed string
	}{
		{
			name: "the version table exists and holds no row",
			seed: `CREATE TABLE accounting_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);` + staleCounters,
		},
		{
			name: "the version table was never written",
			seed: staleCounters,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "accounting.db")
			stale, err := sql.Open("sqlite", path+connectionPragmas)
			if err != nil {
				t.Fatalf("Open(): %v", err)
			}
			if _, err := stale.Exec(testCase.seed); err != nil {
				t.Fatalf("seeding a store with no readable version: %v", err)
			}
			if err := stale.Close(); err != nil {
				t.Fatalf("Close(): %v", err)
			}

			store, err := OpenStore(path)
			if err != nil {
				t.Fatalf("OpenStore(): %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			book := newTestBook(t, 1)
			if err := book.RecordRace(testEscrow, []Attempt{
				{Nonce: 1, Sent: true, Acknowledged: true, Usage: UsageWinner, Terminal: "won"},
			}); err != nil {
				t.Fatalf("RecordRace(): %v", err)
			}
			markFinished(t, book, testEscrow, 1)
			if err := store.Save(context.Background(), book); err != nil {
				t.Fatalf("Save() over a store with no readable version: %v", err)
			}
		})
	}
}
