package storetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/store"
)

type testClock struct {
	mu      sync.Mutex
	current time.Time
}

func countStoredRequests(t *testing.T, testStore store.Store, requestIDs ...string) int {
	t.Helper()
	count := 0
	for _, requestID := range requestIDs {
		_, found, err := testStore.FindRequest(context.Background(), requestID)
		if err != nil {
			t.Fatalf("FindRequest(%s): %v", requestID, err)
		}
		if found {
			count++
		}
	}
	return count
}

func newTestClock(at time.Time) *testClock { return &testClock{current: at} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *testClock) Advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(by)
}

func generousRetention() store.Retention {
	return store.Retention{MaxAge: 30 * 24 * time.Hour, MaxRows: 1_000}
}

func sampleRecord(requestID string) store.RequestRecord {
	return store.RequestRecord{
		RequestID:          requestID,
		EscrowID:           "escrow-7",
		Model:              "qwen",
		Outcome:            store.RequestSettled,
		Decision:           "hedged",
		Stream:             true,
		WinnerNonce:        42,
		WinnerParticipant:  "gonka1participant",
		WinnerHost:         "host-3.gonka.ai",
		WinnerHostIdx:      3,
		Attempts:           2,
		InputTokens:        128,
		InputTokensSource:  filters.InputTokensTokenizerExact,
		WinnerOutputTokens: 256,
		TotalOutputTokens:  381,
		BalanceExhausted:   true,
		StartedAt:          time.Unix(1700000000, 0).UTC(),
		CompletedAt:        time.Unix(1700000004, 0).UTC(),
		FirstTokenMS:       450,
		DurationMS:         4_000,
	}
}

func openLedger(t *testing.T, testStore store.Store, retention store.Retention, clock *testClock) *store.Ledger {
	t.Helper()
	ledger, err := testStore.NewLedger(retention, clock.Now)
	if err != nil {
		t.Fatalf("NewLedger(): %v", err)
	}
	return ledger
}

// Test flow:
//  1. Open a ledger with generous retention and a fixed test clock.
//  2. Record a fully-populated `sampleRecord` and close the ledger.
//  3. Assert `FindRequest` returns the same record with `RecordedAt` set from the clock.
//  4. Assert the ledger's stats report one written row and no losses.
func ledgerWritesEveryFieldItWasGiven(t *testing.T, open Opener) {
	leakcheck.VerifyNone(t)

	testStore := open()
	clock := newTestClock(time.Unix(1700000010, 0).UTC())
	ledger := openLedger(t, testStore, generousRetention(), clock)

	written := sampleRecord("request-1")
	ledger.Record(written)
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	loaded, found, err := testStore.FindRequest(context.Background(), "request-1")
	if err != nil {
		t.Fatalf("FindRequest(): %v", err)
	}
	if !found {
		t.Fatal("FindRequest() found = false, want true")
	}
	expected := written
	expected.RecordedAt = clock.Now()
	if loaded != expected {
		t.Fatalf("FindRequest() = %+v, want %+v", loaded, expected)
	}
	if stats := ledger.Stats(); stats.Written != 1 || stats.Dropped != 0 || stats.Failed != 0 {
		t.Fatalf("Stats() = %+v, want 1 written and no losses", stats)
	}
}

// Test flow:
//  1. Open a ledger and record the same request ID twice.
//  2. Close the ledger.
//  3. Assert the first write is the row that is stored.
func ledgerWritesExactlyOneRowPerRequestID(t *testing.T, open Opener) {
	leakcheck.VerifyNone(t)

	testStore := open()
	clock := newTestClock(time.Unix(1700000010, 0).UTC())
	ledger := openLedger(t, testStore, generousRetention(), clock)

	first := sampleRecord("request-1")
	second := sampleRecord("request-1")
	second.Decision = "second-write"
	ledger.Record(first)
	ledger.Record(second)
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	stored, found, err := testStore.FindRequest(context.Background(), "request-1")
	if err != nil || !found || stored.Decision != first.Decision {
		t.Fatalf("FindRequest() = decision %q, %v, %v; want the first write's %q", stored.Decision, found, err, first.Decision)
	}
}

// Test flow:
//  1. Open a ledger and record one request with no ID set.
//  2. Look up a missing ID and a blank ID via `Find`.
//  3. Assert both report not found.
func ledgerFindReportsUnknownAndBlankIDs(t *testing.T, open Opener) {
	leakcheck.VerifyNone(t)

	testStore := open()
	clock := newTestClock(time.Unix(1700000010, 0).UTC())
	ledger := openLedger(t, testStore, generousRetention(), clock)
	t.Cleanup(func() { _ = ledger.Close() })

	ledger.Record(store.RequestRecord{Outcome: store.RequestFailed})

	for _, requestID := range []string{"missing", ""} {
		_, found, err := ledger.Find(context.Background(), requestID)
		if err != nil {
			t.Fatalf("Find(%q): %v", requestID, err)
		}
		if found {
			t.Fatalf("Find(%q) found = true, want false", requestID)
		}
	}
}

// Test flow:
//  1. Record a stale row under an old clock, close and reopen the store.
//  2. Record a fresh row under a clock 48 hours later, using the same 24-hour max-age retention.
//  3. Assert the stale row is gone and the fresh row survives.
func retentionEvictsRowsPastTheMaxAge(t *testing.T, open Opener) {
	leakcheck.VerifyNone(t)

	retention := store.Retention{MaxAge: 24 * time.Hour, MaxRows: 1_000}

	first := open()
	oldClock := newTestClock(time.Unix(1700000000, 0).UTC())
	oldLedger := openLedger(t, first, retention, oldClock)
	oldLedger.Record(sampleRecord("stale"))
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	second := open()
	defer func() { _ = second.Close() }()
	freshClock := newTestClock(time.Unix(1700000000, 0).UTC().Add(48 * time.Hour))
	freshLedger := openLedger(t, second, retention, freshClock)
	freshLedger.Record(sampleRecord("fresh"))
	if err := freshLedger.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	if _, found, _ := second.FindRequest(context.Background(), "stale"); found {
		t.Fatal("stale row survived its max age")
	}
	if _, found, _ := second.FindRequest(context.Background(), "fresh"); !found {
		t.Fatal("fresh row was evicted")
	}
}

// Test flow:
//  1. Open a ledger with a max-rows retention of 2 and record three rows, advancing the clock between each.
//  2. Close the ledger.
//  3. Assert only 2 rows remain, the oldest evicted and the newest surviving.
func retentionEvictsTheOldestRowsPastMaxRows(t *testing.T, open Opener) {
	leakcheck.VerifyNone(t)

	testStore := open()
	clock := newTestClock(time.Unix(1700000000, 0).UTC())
	ledger := openLedger(t, testStore, store.Retention{MaxAge: 24 * time.Hour, MaxRows: 2}, clock)

	for _, requestID := range []string{"oldest", "middle", "newest"} {
		ledger.Record(sampleRecord(requestID))
		clock.Advance(time.Second)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	count := countStoredRequests(t, testStore, "oldest", "middle", "newest")
	if count != 2 {
		t.Fatalf("accounting rows = %d, want 2", count)
	}
	if _, found, _ := testStore.FindRequest(context.Background(), "oldest"); found {
		t.Fatal("oldest row survived the row-count bound")
	}
	if _, found, _ := testStore.FindRequest(context.Background(), "newest"); !found {
		t.Fatal("newest row was evicted")
	}
}

// Test flow:
//  1. Build a strictly increasing sequence of timestamps, including ones a nanosecond, a tenth of a second, and a whole second apart.
//  2. Format each with `store.FormatTime`.
//  3. Assert every formatted timestamp sorts as text strictly before the next, since retention compares these strings byte by byte.
func storedTimestampsSortInTimeOrder(t *testing.T, open Opener) {
	base := time.Date(2026, 8, 2, 3, 0, 5, 0, time.UTC)
	ascending := []time.Time{
		base,
		base.Add(time.Nanosecond),
		base.Add(100 * time.Millisecond),
		base.Add(time.Second),
		base.Add(time.Minute),
	}

	for index := 1; index < len(ascending); index++ {
		earlier, later := store.FormatTime(ascending[index-1]), store.FormatTime(ascending[index])
		if earlier >= later {
			t.Fatalf("%q does not sort before %q, so a text-ordered sweep prunes out of order", earlier, later)
		}
	}
}

// Test flow:
//  1. Open a ledger that keeps two rows and record three requests a minute apart.
//  2. Close the ledger, which sweeps.
//  3. Assert the oldest request is gone and the other two read back with every field.
func ledgerKeepsTheNewestRows(t *testing.T, open Opener) {
	gatewayStore := open()
	clock := newTestClock(time.Unix(1_700_000_010, 0).UTC())
	ledger := openLedger(t, gatewayStore, store.Retention{MaxAge: 24 * time.Hour, MaxRows: 2}, clock)
	ids := []string{"request-1", "request-2", "request-3"}
	written := make([]store.RequestRecord, 0, len(ids))
	for _, id := range ids {
		record := sampleRecord(id)
		ledger.Record(record)
		record.RecordedAt = clock.Now()
		written = append(written, record)
		clock.Advance(time.Minute)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, found, _ := gatewayStore.FindRequest(context.Background(), "request-1"); found {
		t.Fatal("request-1 survived the row cap, want it swept")
	}
	for _, want := range written[1:] {
		got, found, err := gatewayStore.FindRequest(context.Background(), want.RequestID)
		if err != nil || !found || got != want {
			t.Fatalf("FindRequest(%s) = %+v, %v, %v; want %+v", want.RequestID, got, found, err, want)
		}
	}
}
