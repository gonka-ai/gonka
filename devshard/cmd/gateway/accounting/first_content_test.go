package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

const (
	otherModel  = "model-b"
	thirdEscrow = "escrow-3"
)

type delayGroup struct {
	delay time.Duration
	count int
}

func recordFirstContent(t *testing.T, book *Book, attempts ...Attempt) {
	t.Helper()
	recordFirstContentOn(t, book, testEscrow, attempts...)
}

func recordFirstContentOn(t *testing.T, book *Book, escrowID string, attempts ...Attempt) {
	t.Helper()
	for index := range attempts {
		attempts[index].RequestID = "request-1"
		attempts[index].Sent = true
		attempts[index].Usage = UsageLoser
	}
	if err := book.RecordRace(escrowID, attempts); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
}

func recordOf(t *testing.T, book *Book, participant, model string) ParticipantRecord {
	t.Helper()
	for _, record := range book.Query(QueryFilter{}) {
		if record.Participant == participant && record.Model == model {
			return record
		}
	}
	t.Fatalf("no record for participant %s on %s", participant, model)
	return ParticipantRecord{}
}

func onlyEpoch(t *testing.T, book *Book) EpochSummary {
	t.Helper()
	epochs := book.Epochs(QueryFilter{})
	if len(epochs) != 1 {
		t.Fatalf("Epochs() = %d summaries, want 1", len(epochs))
	}
	return epochs[0]
}

func modelOf(t *testing.T, epoch EpochSummary, model string) ModelFirstContent {
	t.Helper()
	for _, entry := range epoch.FirstContentByModel {
		if entry.Model == model {
			return entry
		}
	}
	t.Fatalf("epoch %d carries no first content for %s: %+v", epoch.EpochIndex, model, epoch.FirstContentByModel)
	return ModelFirstContent{}
}

func assertTiming(t *testing.T, level string, timing FirstContentTiming, total, attempts uint64) {
	t.Helper()
	if timing.FirstContentMillisecondsTotal != total || timing.FirstContentAttempts != attempts {
		t.Fatalf("%s first content = %d ms over %d attempts, want %d ms over %d",
			level, timing.FirstContentMillisecondsTotal, timing.FirstContentAttempts, total, attempts)
	}
}

func assertModel(t *testing.T, entry ModelFirstContent, total, attempts uint64, buckets string, percentiles [3]uint64) {
	t.Helper()
	assertTiming(t, entry.Model, entry.FirstContentTiming, total, attempts)
	if got := mustJSON(t, entry.FirstContentBuckets); got != buckets {
		t.Fatalf("%s first_content_buckets = %s, want %s", entry.Model, got, buckets)
	}
	got := [3]uint64{entry.FirstContentP50Milliseconds, entry.FirstContentP95Milliseconds, entry.FirstContentP99Milliseconds}
	if got != percentiles {
		t.Fatalf("%s p50/p95/p99 = %v ms, want %v", entry.Model, got, percentiles)
	}
}

func timedBook(t *testing.T) *Book {
	t.Helper()
	book := newTestBook(t, 4)
	recordFirstContent(t, book,
		Attempt{Nonce: 5, FirstContentDelay: 300 * time.Millisecond},
		Attempt{Nonce: 9, FirstContentDelay: 500 * time.Millisecond},
		Attempt{Nonce: 6},
	)
	return book
}

func attemptsOf(groups ...delayGroup) []Attempt {
	var attempts []Attempt
	for _, group := range groups {
		for range group.count {
			attempts = append(attempts, Attempt{Nonce: uint64(1 + 4*len(attempts)), FirstContentDelay: group.delay})
		}
	}
	return attempts
}

// Test flow:
//  1. Record a race whose two attempts on slot 1 reached first content 300 ms and 500 ms after their sends, and whose attempt on slot 2 sent no content.
//  2. Assert slot 1's participant record and the epoch summary carry 800 ms over 2 timed attempts and slot 2's record carries none.
//  3. Assert the epoch's entry for the model carries the same timing, the 355 ms and 555 ms buckets, p50 355, p95 555 and p99 555.
func TestTheTimeToFirstContentReachesTheParticipantTheEpochAndTheModel(t *testing.T) {
	book := timedBook(t)

	assertTiming(t, "slot 1", recordOf(t, book, participantFor(1), testModel).FirstContentTiming, 800, 2)
	assertTiming(t, "slot 2", recordOf(t, book, participantFor(2), testModel).FirstContentTiming, 0, 0)
	epoch := onlyEpoch(t, book)
	assertTiming(t, "epoch", epoch.FirstContentTiming, 800, 2)
	assertModel(t, modelOf(t, epoch, testModel), 800, 2, `{"355":1,"555":1}`, [3]uint64{355, 555, 555})
}

// Test flow:
//  1. For each delay, record one attempt with it on a fresh book.
//  2. Assert the model's buckets hold exactly one attempt under the expected upper bound, the overflow key past the largest bound.
func TestADelayLandsInTheBucketOfTheSmallestBoundItDoesNotExceed(t *testing.T) {
	for _, test := range []struct {
		delay time.Duration
		want  string
	}{
		{time.Millisecond, `{"10":1}`},
		{10 * time.Millisecond, `{"10":1}`},
		{10*time.Millisecond + time.Microsecond, `{"13":1}`},
		{250 * time.Millisecond, `{"284":1}`},
		{284 * time.Millisecond, `{"284":1}`},
		{285 * time.Millisecond, `{"355":1}`},
		{2 * time.Second, `{"2118":1}`},
		{9 * time.Minute, `{"560519":1}`},
		{560519 * time.Millisecond, `{"560519":1}`},
		{560520 * time.Millisecond, `{"+Inf":1}`},
		{time.Hour, `{"+Inf":1}`},
	} {
		book := newTestBook(t, 4)
		recordFirstContent(t, book, Attempt{Nonce: 5, FirstContentDelay: test.delay})

		if got := mustJSON(t, modelOf(t, onlyEpoch(t, book), testModel).FirstContentBuckets); got != test.want {
			t.Fatalf("buckets after a %v delay = %s, want %s", test.delay, got, test.want)
		}
	}
}

// Test flow:
//  1. For 1, 2, 20 and 100 timed attempts with known delays, record them on slot 1 of a fresh book.
//  2. Assert the model's p50, p95 and p99 are the upper bounds of the buckets holding the nearest ranks, and its total and buckets hold every attempt.
func TestThePercentilesAreTheUpperBoundOfTheBucketHoldingTheNearestRank(t *testing.T) {
	for _, test := range []struct {
		name        string
		groups      []delayGroup
		total       uint64
		buckets     string
		percentiles [3]uint64
	}{
		{"one attempt", []delayGroup{{300 * time.Millisecond, 1}}, 300, `{"355":1}`, [3]uint64{355, 355, 355}},
		{"two attempts", []delayGroup{{300 * time.Millisecond, 1}, {500 * time.Millisecond, 1}}, 800, `{"355":1,"555":1}`, [3]uint64{355, 555, 555}},
		{
			"twenty attempts",
			[]delayGroup{{50 * time.Millisecond, 10}, {200 * time.Millisecond, 9}, {2 * time.Second, 1}},
			10*50 + 9*200 + 2000, `{"2118":1,"227":9,"60":10}`, [3]uint64{60, 227, 2118},
		},
		{
			"hundred attempts",
			[]delayGroup{{50 * time.Millisecond, 50}, {200 * time.Millisecond, 44}, {2 * time.Second, 4}, {20 * time.Second, 2}},
			50*50 + 44*200 + 4*2000 + 2*20000, `{"2118":4,"227":44,"24652":2,"60":50}`, [3]uint64{60, 2118, 24652},
		},
	} {
		book := newTestBook(t, 4)
		attempts := attemptsOf(test.groups...)
		recordFirstContent(t, book, attempts...)

		assertModel(t, modelOf(t, onlyEpoch(t, book), testModel), test.total, uint64(len(attempts)), test.buckets, test.percentiles)
	}
}

// Test flow:
//  1. Record one attempt past the largest bound.
//  2. Assert it is counted in the overflow bucket and every percentile reads as the largest bound.
func TestAnAttemptPastTheLargestBoundReadsAsTheLargestBound(t *testing.T) {
	book := newTestBook(t, 4)
	recordFirstContent(t, book, Attempt{Nonce: 5, FirstContentDelay: 20 * time.Minute})

	assertModel(t, modelOf(t, onlyEpoch(t, book), testModel),
		uint64((20 * time.Minute).Milliseconds()), 1, `{"+Inf":1}`, [3]uint64{560519, 560519, 560519})
}

// Test flow:
//  1. Open escrow-1 (participants A to D) and escrow-2 (participants E to H) on model-a, and escrow-3 (participants A to D) on model-b, all in one epoch.
//  2. Record 50 ms on slot 1 and 200 ms on slot 2 of escrow-1, 50 ms and 2 s on slot 1 of escrow-2, and 1, 5 and 20 s on slot 1 of escrow-3.
//  3. Assert model-a's entry merges both of its escrows' buckets, model-b's holds only escrow-3, and the entries come out in model order.
//  4. Assert participant B keeps one sum and count per model, and the epoch sums every timed attempt.
func TestTheBucketsRollUpIntoTheModelOfTheirEscrow(t *testing.T) {
	book := newTestBook(t, 4)
	openModelEscrow(t, book, otherEscrow, testModel, testEpoch, 4, 4)
	openModelEscrow(t, book, thirdEscrow, otherModel, testEpoch, 4, 0)
	recordFirstContentOn(t, book, testEscrow,
		Attempt{Nonce: 5, FirstContentDelay: 50 * time.Millisecond},
		Attempt{Nonce: 6, FirstContentDelay: 200 * time.Millisecond},
	)
	recordFirstContentOn(t, book, otherEscrow,
		Attempt{Nonce: 5, FirstContentDelay: 50 * time.Millisecond},
		Attempt{Nonce: 9, FirstContentDelay: 2 * time.Second},
	)
	recordFirstContentOn(t, book, thirdEscrow,
		Attempt{Nonce: 5, FirstContentDelay: time.Second},
		Attempt{Nonce: 9, FirstContentDelay: 5 * time.Second},
		Attempt{Nonce: 13, FirstContentDelay: 20 * time.Second},
	)

	epoch := onlyEpoch(t, book)
	if len(epoch.FirstContentByModel) != 2 || epoch.FirstContentByModel[0].Model != testModel || epoch.FirstContentByModel[1].Model != otherModel {
		t.Fatalf("first_content_by_model = %+v, want model-a then model-b", epoch.FirstContentByModel)
	}
	assertModel(t, epoch.FirstContentByModel[0], 2300, 4, `{"2118":1,"227":1,"60":2}`, [3]uint64{60, 2118, 2118})
	assertModel(t, epoch.FirstContentByModel[1], 26000, 3, `{"1084":1,"24652":1,"5170":1}`, [3]uint64{5170, 24652, 24652})
	assertTiming(t, "participant B on model-a", recordOf(t, book, participantFor(1), testModel).FirstContentTiming, 50, 1)
	assertTiming(t, "participant B on model-b", recordOf(t, book, participantFor(1), otherModel).FirstContentTiming, 26000, 3)
	assertTiming(t, "participant F on model-a", recordOf(t, book, participantFor(5), testModel).FirstContentTiming, 2050, 2)
	assertTiming(t, "epoch", epoch.FirstContentTiming, 28300, 7)
}

// Test flow:
//  1. Open escrow-1 on model-a in epoch 7, escrow-2 on model-a in epoch 8 and escrow-3 on model-b in epoch 7, and time one attempt on each.
//  2. Assert each epoch's model-a entry holds only its own escrow's attempt.
//  3. For a read filtered to model-b and one filtered to escrow-3, assert it serves epoch 7 with the model-b entry alone.
func TestAModelsDistributionStaysWithinItsEpochAndTheFiltersNarrowTheModelList(t *testing.T) {
	book := newTestBook(t, 4)
	openTestEscrow(t, book, otherEscrow, testEpoch+1, 4)
	openModelEscrow(t, book, thirdEscrow, otherModel, testEpoch, 4, 0)
	recordFirstContent(t, book, Attempt{Nonce: 5, FirstContentDelay: 50 * time.Millisecond})
	recordFirstContentOn(t, book, otherEscrow, Attempt{Nonce: 5, FirstContentDelay: 2 * time.Second})
	recordFirstContentOn(t, book, thirdEscrow, Attempt{Nonce: 5, FirstContentDelay: time.Second})

	epochs := book.Epochs(QueryFilter{})

	if len(epochs) != 2 {
		t.Fatalf("Epochs() = %d summaries, want 2", len(epochs))
	}
	assertModel(t, modelOf(t, epochs[0], testModel), 50, 1, `{"60":1}`, [3]uint64{60, 60, 60})
	assertModel(t, modelOf(t, epochs[1], testModel), 2000, 1, `{"2118":1}`, [3]uint64{2118, 2118, 2118})
	for _, filter := range []QueryFilter{{Model: otherModel}, {EscrowIDs: []string{thirdEscrow}}} {
		filtered := book.Epochs(filter)
		if len(filtered) != 1 || filtered[0].EpochIndex != testEpoch ||
			len(filtered[0].FirstContentByModel) != 1 || filtered[0].FirstContentByModel[0].Model != otherModel {
			t.Fatalf("Epochs(%+v) = %+v, want epoch 7 with model-b alone", filter, filtered)
		}
		assertModel(t, filtered[0].FirstContentByModel[0], 1000, 1, `{"1084":1}`, [3]uint64{1084, 1084, 1084})
	}
}

// Test flow:
//  1. Record the same attempt with a 300 ms time to first content twice.
//  2. Assert its participant and its model carry 300 ms over 1 attempt in one bucket.
func TestAnAttemptReportedTwiceIsTimedOnce(t *testing.T) {
	book := newTestBook(t, 4)

	for range 2 {
		recordFirstContent(t, book, Attempt{Nonce: 5, FirstContentDelay: 300 * time.Millisecond})
	}

	assertTiming(t, "participant", recordOf(t, book, participantFor(1), testModel).FirstContentTiming, 300, 1)
	assertModel(t, modelOf(t, onlyEpoch(t, book), testModel), 300, 1, `{"355":1}`, [3]uint64{355, 355, 355})
}

// Test flow:
//  1. Take a snapshot and an epochs read of a book with one timed attempt.
//  2. Record a second attempt in the same bucket.
//  3. Assert the snapshot and the epochs read still hold one attempt in that bucket.
func TestASnapshotAndAnEpochsReadDoNotShareTheLiveBuckets(t *testing.T) {
	book := newTestBook(t, 4)
	recordFirstContent(t, book, Attempt{Nonce: 5, FirstContentDelay: 300 * time.Millisecond})
	snapshot := book.Snapshot()
	read := modelOf(t, onlyEpoch(t, book), testModel)

	recordFirstContent(t, book, Attempt{Nonce: 9, FirstContentDelay: 300 * time.Millisecond})

	if got := mustJSON(t, snapshot.Escrows[0].FirstContentBuckets); got != `{"355":1}` {
		t.Fatalf("snapshot buckets = %s, want the one attempt it was taken with", got)
	}
	if got := mustJSON(t, read.FirstContentBuckets); got != `{"355":1}` {
		t.Fatalf("epochs read buckets = %s, want the one attempt it was read with", got)
	}
}

// Test flow:
//  1. Record two timed attempts on slot 1 of a model-a escrow totalling 800 ms, one untimed attempt on slot 2, and two 2 s attempts and one 20 minute attempt on a model-b escrow.
//  2. Save and reload the book.
//  3. Assert the participant keeps its sum and count and each model keeps its own buckets and percentiles, the overflow bucket included.
func TestTheTimeToFirstContentSurvivesARestart(t *testing.T) {
	book := timedBook(t)
	openModelEscrow(t, book, otherEscrow, otherModel, testEpoch, 4, 0)
	recordFirstContentOn(t, book, otherEscrow,
		Attempt{Nonce: 5, FirstContentDelay: 2 * time.Second},
		Attempt{Nonce: 9, FirstContentDelay: 2 * time.Second},
		Attempt{Nonce: 13, FirstContentDelay: 20 * time.Minute},
	)

	restored := saveAndReload(t, book, openTestStore(t))

	assertTiming(t, "participant", recordOf(t, restored, participantFor(1), testModel).FirstContentTiming, 800, 2)
	epoch := onlyEpoch(t, restored)
	assertModel(t, modelOf(t, epoch, testModel), 800, 2, `{"355":1,"555":1}`, [3]uint64{355, 555, 555})
	assertModel(t, modelOf(t, epoch, otherModel), 4000+uint64((20*time.Minute).Milliseconds()), 3,
		`{"+Inf":1,"2118":2}`, [3]uint64{2118, 560519, 560519})
}

// Test flow:
//  1. Record a 300 ms attempt and save, then record a 500 ms attempt and save again.
//  2. Reload the book.
//  3. Assert the participant and the model carry both attempts once each.
func TestTwoSavesOfATimedEscrowReloadAsTheirSum(t *testing.T) {
	book := newTestBook(t, 4)
	store := openTestStore(t)
	recordFirstContent(t, book, Attempt{Nonce: 5, FirstContentDelay: 300 * time.Millisecond})
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	recordFirstContent(t, book, Attempt{Nonce: 9, FirstContentDelay: 500 * time.Millisecond})

	restored := saveAndReload(t, book, store)

	assertTiming(t, "participant", recordOf(t, restored, participantFor(1), testModel).FirstContentTiming, 800, 2)
	assertModel(t, modelOf(t, onlyEpoch(t, restored), testModel), 800, 2, `{"355":1,"555":1}`, [3]uint64{355, 555, 555})
}

func storedFirstContentRows(t *testing.T, db *sql.DB, escrowID string) int {
	t.Helper()
	total := 0
	for _, table := range firstContentTables {
		var rows int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE escrow_id = ?`, escrowID).Scan(&rows); err != nil {
			t.Fatalf("counting %s rows of %s: %v", table, escrowID, err)
		}
		total += rows
	}
	return total
}

// Test flow:
//  1. For a prune and for a reset: open a timed escrow-1 and an untimed escrow-2, save, and assert escrow-1 has first-content rows.
//  2. Retire and prune escrow-1, or reset its epoch, and save again.
//  3. Assert no first-content row of escrow-1 is left in the store and no model entry is served for its epoch.
func TestAnEscrowLeavingTheBookTakesItsFirstContentRowsWithIt(t *testing.T) {
	for name, remove := range map[string]func(*Book){
		"prune": func(book *Book) { book.RetireEscrow(testEscrow); book.PruneBefore(testEpoch + 5) },
		"reset": func(book *Book) { book.ResetEpoch(testEpoch) },
	} {
		book := timedBook(t)
		openTestEscrow(t, book, otherEscrow, testEpoch+5, 4)
		store := openTestStore(t)
		if err := store.Save(context.Background(), book); err != nil {
			t.Fatalf("%s: Save(): %v", name, err)
		}
		if rows := storedFirstContentRows(t, store.db, testEscrow); rows != 3 {
			t.Fatalf("%s: first-content rows before = %d, want 1 timing row and 2 bucket rows", name, rows)
		}

		remove(book)
		restored := saveAndReload(t, book, store)

		if rows := storedFirstContentRows(t, store.db, testEscrow); rows != 0 {
			t.Fatalf("%s: first-content rows after = %d, want none", name, rows)
		}
		if epochs := restored.Epochs(QueryFilter{EpochIndex: testEpoch}); len(epochs) != 0 {
			t.Fatalf("%s: epoch %d after = %+v, want none", name, testEpoch, epochs)
		}
	}
}

func saveTimedStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounting.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore(): %v", err)
	}
	book := timedBook(t)
	if err := book.RecordGhost(testEscrow, 7, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	if err := store.Save(context.Background(), book); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	return path
}

func rewriteStore(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening the store as another build left it: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func reopenStore(t *testing.T, path string) (*Store, *Book) {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() over the rewritten store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if len(snapshot.Escrows) != 1 || len(snapshot.Escrows[0].Counters) == 0 {
		t.Fatalf("the reopened store holds %+v, want the escrow and counters it was saved with", snapshot.Escrows)
	}
	restored := NewBook(func() time.Time { return time.Unix(0, 0).UTC() })
	if err := restored.Restore(snapshot); err != nil {
		t.Fatalf("Restore(): %v", err)
	}
	return store, restored
}

func assertUntimed(t *testing.T, book *Book) {
	t.Helper()
	assertTiming(t, "participant", recordOf(t, book, participantFor(1), testModel).FirstContentTiming, 0, 0)
	assertModel(t, modelOf(t, onlyEpoch(t, book), testModel), 0, 0, `null`, [3]uint64{})
}

// Test flow:
//  1. Save a book holding a ghost and two timed attempts, then drop both first-content tables and their meta key as a store written before them.
//  2. Reopen and load the store.
//  3. Assert the escrow and its counters are kept and the restored ledger carries no first-content timing.
func TestAStoreWrittenBeforeTheFirstContentTablesIsKeptRatherThanDropped(t *testing.T) {
	path := saveTimedStore(t)
	rewriteStore(t, path,
		`DROP TABLE accounting_first_content`,
		`DROP TABLE accounting_first_content_distribution`,
		`DELETE FROM accounting_meta WHERE key = 'first_content_distribution_written_at'`,
	)

	_, restored := reopenStore(t, path)

	assertUntimed(t, restored)
}

// Test flow:
//  1. Save a timed book, then move updated_at on as a save by a build without the first-content tables would.
//  2. Reopen and load the store.
//  3. Assert the escrow is kept, its timing starts at zero and no first-content row is left.
func TestFirstContentRowsAnotherBuildSavedAroundAreDiscarded(t *testing.T) {
	path := saveTimedStore(t)
	rewriteStore(t, path, `UPDATE accounting_meta SET value = '2001-01-01T00:00:00Z' WHERE key = 'updated_at'`)

	store, restored := reopenStore(t, path)

	assertUntimed(t, restored)
	if rows := storedFirstContentRows(t, store.db, testEscrow); rows != 0 {
		t.Fatalf("first-content rows = %d, want none", rows)
	}
}

// Test flow:
//  1. Save a timed book, then reshape the store as a build that kept the buckets per slot leaves it: its rows in accounting_first_content_buckets, no distribution table, and first_content_written_at equal to updated_at.
//  2. Reopen and load the store, record a 300 ms attempt, then save and reload the book.
//  3. Assert the reopened ledger carries no first-content timing and the reloaded one carries the new attempt alone.
//  4. Close the store, reopen and load it again.
//  5. Assert the ledger loaded from the reopened store still carries the 300 ms attempt alone.
func TestAStoreWithTheBucketsKeptPerSlotStartsItsTimingAtZero(t *testing.T) {
	path := saveTimedStore(t)
	rewriteStore(t, path,
		`DROP TABLE accounting_first_content_distribution`,
		`CREATE TABLE accounting_first_content_buckets (escrow_id TEXT NOT NULL, slot_id INTEGER NOT NULL, bucket INTEGER NOT NULL, attempts INTEGER NOT NULL, PRIMARY KEY (escrow_id, slot_id, bucket))`,
		`INSERT INTO accounting_first_content_buckets VALUES ('escrow-1', 1, 18, 1), ('escrow-1', 1, 20, 1)`,
		`DELETE FROM accounting_meta WHERE key = 'first_content_distribution_written_at'`,
		`INSERT OR REPLACE INTO accounting_meta (key, value) SELECT 'first_content_written_at', value FROM accounting_meta WHERE key = 'updated_at'`,
	)

	store, restored := reopenStore(t, path)

	assertUntimed(t, restored)
	recordFirstContent(t, restored, Attempt{Nonce: 13, FirstContentDelay: 300 * time.Millisecond})
	reloaded := saveAndReload(t, restored, store)
	assertTiming(t, "participant", recordOf(t, reloaded, participantFor(1), testModel).FirstContentTiming, 300, 1)
	assertModel(t, modelOf(t, onlyEpoch(t, reloaded), testModel), 300, 1, `{"355":1}`, [3]uint64{355, 355, 355})

	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	_, reopened := reopenStore(t, path)

	assertTiming(t, "participant", recordOf(t, reopened, participantFor(1), testModel).FirstContentTiming, 300, 1)
	assertModel(t, modelOf(t, onlyEpoch(t, reopened), testModel), 300, 1, `{"355":1}`, [3]uint64{355, 355, 355})
}

// Test flow:
//  1. Save a timed book, then add distribution rows for its escrow at indexes 51, 255 and 256, past the overflow bucket at 50.
//  2. Reopen and load the store.
//  3. Assert the model keeps the buckets it was saved with and none of the added rows.
func TestABucketIndexPastTheOverflowBucketIsSkippedOnLoad(t *testing.T) {
	path := saveTimedStore(t)
	rewriteStore(t, path, `INSERT INTO accounting_first_content_distribution VALUES ('escrow-1', 51, 7), ('escrow-1', 255, 7), ('escrow-1', 256, 7)`)

	_, restored := reopenStore(t, path)

	assertModel(t, modelOf(t, onlyEpoch(t, restored), testModel), 800, 2, `{"355":1,"555":1}`, [3]uint64{355, 555, 555})
}

// Test flow:
//  1. Save a timed book, then add first-content rows for an escrow the store does not hold, as a prune by a build without the tables leaves them.
//  2. Reopen and load the store.
//  3. Assert the stray rows are gone and the held escrow keeps its timing.
func TestFirstContentRowsOfAnEscrowTheStoreDoesNotHoldAreDiscarded(t *testing.T) {
	path := saveTimedStore(t)
	rewriteStore(t, path,
		`INSERT INTO accounting_first_content VALUES ('escrow-gone', 1, 300, 1)`,
		`INSERT INTO accounting_first_content_distribution VALUES ('escrow-gone', 16, 1)`,
	)

	store, restored := reopenStore(t, path)

	if rows := storedFirstContentRows(t, store.db, "escrow-gone"); rows != 0 {
		t.Fatalf("stray first-content rows = %d, want none", rows)
	}
	assertTiming(t, "participant", recordOf(t, restored, participantFor(1), testModel).FirstContentTiming, 800, 2)
	assertModel(t, modelOf(t, onlyEpoch(t, restored), testModel), 800, 2, `{"355":1,"555":1}`, [3]uint64{355, 555, 555})
}

func decodeBody(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
}

func assertServedFirstContent(t *testing.T, level string, object map[string]any, want map[string]any) {
	t.Helper()
	if object == nil {
		t.Fatalf("no %s object in the response", level)
	}
	for field, value := range want {
		got, present := object[field]
		if !present {
			t.Fatalf("%s carries no %s", level, field)
		}
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, value); gotJSON != wantJSON {
			t.Fatalf("%s %s = %s, want %s", level, field, gotJSON, wantJSON)
		}
	}
}

func assertNoDistribution(t *testing.T, level string, object map[string]any) {
	t.Helper()
	for _, field := range []string{"first_content_buckets", "first_content_p50_ms", "first_content_p95_ms", "first_content_p99_ms", "first_content_by_model"} {
		if value, present := object[field]; present {
			t.Fatalf("%s carries %s = %v, want only the sum and the count", level, field, value)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding %v: %v", value, err)
	}
	return string(encoded)
}

// Test flow:
//  1. Record two timed attempts on slot 1, 300 ms and 500 ms.
//  2. Request the current-epoch participants route and assert slot 1's participant object and each of its slot objects carry the sum and the count and nothing more.
//  3. Request the epochs route and assert the epoch object carries the sum and the count, and one first_content_by_model entry with exactly the model, the sum, the count, the buckets and the three percentiles.
func TestTheSurfaceServesTheDistributionPerModelAndOnlyTheMeanPerParticipant(t *testing.T) {
	book := timedBook(t)
	timing := map[string]any{"first_content_ms_total": 800, "first_content_attempts": 2}

	var participants struct {
		Participants []map[string]any `json:"participants"`
	}
	decodeBody(t, serve(t, book, "/api/v1/epochs/current/participants").Body.Bytes(), &participants)
	var timed map[string]any
	for _, participant := range participants.Participants {
		if participant["participant"] == participantFor(1) {
			timed = participant
		}
	}
	assertServedFirstContent(t, "participant", timed, timing)
	assertNoDistribution(t, "participant", timed)
	slots, _ := timed["slots"].([]any)
	if len(slots) != 1 {
		t.Fatalf("participant slots = %v, want one", timed["slots"])
	}
	slot, _ := slots[0].(map[string]any)
	assertServedFirstContent(t, "slot", slot, timing)
	assertNoDistribution(t, "slot", slot)

	var epochs struct {
		Epochs []map[string]any `json:"epochs"`
	}
	decodeBody(t, serve(t, book, "/api/v1/epochs").Body.Bytes(), &epochs)
	if len(epochs.Epochs) != 1 {
		t.Fatalf("epochs route = %d summaries, want 1", len(epochs.Epochs))
	}
	assertServedFirstContent(t, "epoch", epochs.Epochs[0], timing)
	byModel := mustJSON(t, epochs.Epochs[0]["first_content_by_model"])
	want := `[{"first_content_attempts":2,"first_content_buckets":{"355":1,"555":1},"first_content_ms_total":800,` +
		`"first_content_p50_ms":355,"first_content_p95_ms":555,"first_content_p99_ms":555,"model":"model-a"}]`
	if byModel != want {
		t.Fatalf("first_content_by_model = %s, want %s", byModel, want)
	}
}

// Test flow:
//  1. Record a 300 ms, a 500 ms and a 20 minute attempt on slot 1 and request the epochs route.
//  2. Decode the response into the ledger's own summary type.
//  3. Assert the decoded model buckets are the served ones, overflow included.
//  4. Assert a bucket key that is not one of the bounds is refused.
func TestAServedEpochDecodesIntoTheLedgersOwnType(t *testing.T) {
	book := timedBook(t)
	recordFirstContent(t, book, Attempt{Nonce: 13, FirstContentDelay: 20 * time.Minute})

	var body struct {
		Epochs []EpochSummary `json:"epochs"`
	}
	decodeBody(t, serve(t, book, "/api/v1/epochs").Body.Bytes(), &body)

	if len(body.Epochs) != 1 {
		t.Fatalf("epochs route = %d summaries, want 1", len(body.Epochs))
	}
	if got := mustJSON(t, modelOf(t, body.Epochs[0], testModel).FirstContentBuckets); got != `{"+Inf":1,"355":1,"555":1}` {
		t.Fatalf("decoded buckets = %s, want the served ones", got)
	}
	var decoded FirstContentBuckets
	if err := json.Unmarshal([]byte(`{"356":1}`), &decoded); err == nil {
		t.Fatalf("decoding a key between two bounds = %v, want an error", decoded)
	}
}

// Test flow:
//  1. Record one untimed attempt and request one participant's route and the epochs route.
//  2. Assert the participant record carries the sum and the count at zero.
//  3. Assert the epoch lists its model with zero attempts, zero percentiles and no buckets.
func TestTheSurfaceServesAZeroTimingRatherThanOmittingIt(t *testing.T) {
	book := newTestBook(t, 4)
	recordFirstContent(t, book, Attempt{Nonce: 5})

	recorder := serve(t, book, "/api/v1/epochs/current/participants/"+participantFor(1))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var body struct {
		Records []map[string]any `json:"records"`
	}
	decodeBody(t, recorder.Body.Bytes(), &body)
	if len(body.Records) != 1 {
		t.Fatalf("participant route = %d records, want 1", len(body.Records))
	}
	assertServedFirstContent(t, "participant", body.Records[0], map[string]any{"first_content_ms_total": 0, "first_content_attempts": 0})
	var epochs struct {
		Epochs []map[string]any `json:"epochs"`
	}
	decodeBody(t, serve(t, book, "/api/v1/epochs").Body.Bytes(), &epochs)
	byModel := mustJSON(t, epochs.Epochs[0]["first_content_by_model"])
	want := `[{"first_content_attempts":0,"first_content_ms_total":0,` +
		`"first_content_p50_ms":0,"first_content_p95_ms":0,"first_content_p99_ms":0,"model":"model-a"}]`
	if byModel != want {
		t.Fatalf("first_content_by_model = %s, want %s", byModel, want)
	}
}
