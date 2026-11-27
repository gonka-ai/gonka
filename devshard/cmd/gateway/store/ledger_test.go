package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/logging"
)

const stallBound = 5 * time.Second

func testRetention() Retention {
	return Retention{MaxAge: 24 * time.Hour, MaxRows: 100}
}

func fixedClock() func() time.Time {
	at := time.Unix(1_700_000_000, 0).UTC()
	return func() time.Time { return at }
}

// Test flow:
//  1. Stall the backend's insert, open a ledger and record more rows than its queue holds from a goroutine.
//  2. Assert `Record` returns within a bound rather than blocking behind the writer, and that shed rows are counted as dropped.
//  3. Release the writer and close the ledger.
//  4. Assert `Close` returns nil and the dropped count survives it.
func TestRecordDoesNotBlockWhileTheWriterIsStalled(t *testing.T) {
	backend := &fakeBackend{hold: make(chan struct{})}
	ledger, err := New(backend).NewLedger(testRetention(), fixedClock())
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		for index := range ledgerQueueDepth * 3 {
			ledger.Record(RequestRecord{RequestID: string(rune('a'+index%26)) + string(rune('a'+index/26))})
		}
	}()
	select {
	case <-returned:
	case <-time.After(stallBound):
		t.Fatal("Record() blocked behind the stalled writer for 5s")
	}
	if ledger.Stats().Dropped == 0 {
		t.Fatal("Stats().Dropped = 0, want the shed rows counted")
	}
	close(backend.hold)
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if ledger.Stats().Dropped == 0 {
		t.Fatal("Stats().Dropped after Close() = 0, want the shed rows still counted")
	}
}

type capturedLogger struct {
	mu    sync.Mutex
	warns []string
}

func (c *capturedLogger) Warn(msg string, keyvals ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warns = append(c.warns, fmt.Sprint(append([]any{msg}, keyvals...)...))
}

func (c *capturedLogger) Info(string, ...any)  {}
func (c *capturedLogger) Error(string, ...any) {}
func (c *capturedLogger) Debug(string, ...any) {}

// Test flow:
//  1. Capture the log, make the backend refuse inserts and record two rows with different request ids.
//  2. Close the ledger and assert the failed counter is 2.
//  3. Assert exactly one warning per lost row, each naming its request id, escrow, model and the insert error.
func TestEveryLostRowIsLoggedOnceWithItsIdentifiers(t *testing.T) {
	captured := &capturedLogger{}
	logging.SetLogger(captured)
	t.Cleanup(func() { logging.SetLogger(logging.NewSlogAdapter()) })
	backend := &fakeBackend{insertErr: errRefused}
	ledger, err := New(backend).NewLedger(testRetention(), fixedClock())
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}
	ledger.Record(RequestRecord{RequestID: "request-1", EscrowID: "escrow-1", Model: "model-1", WinnerNonce: 7})
	ledger.Record(RequestRecord{RequestID: "request-2", EscrowID: "escrow-2", Model: "model-2"})
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if got := ledger.Stats().Failed; got != 2 {
		t.Fatalf("Stats().Failed = %d, want 2", got)
	}
	if len(captured.warns) != 2 {
		t.Fatalf("warnings = %q, want one per lost row", captured.warns)
	}
	for index, want := range []string{"request-1", "escrow-1", "model-1", errRefused.Error()} {
		if !strings.Contains(captured.warns[0], want) {
			t.Errorf("warning %d = %q, want it to carry %q", index, captured.warns[0], want)
		}
	}
	if !strings.Contains(captured.warns[1], "request-2") {
		t.Errorf("second warning = %q, want it to carry request-2", captured.warns[1])
	}
}

// Test flow:
//  1. Make the backend refuse inserts and record one row.
//  2. Close the ledger and assert the row is counted as failed, not written, and `Close` still returns nil.
func TestARefusedInsertIsCountedNotReturned(t *testing.T) {
	backend := &fakeBackend{insertErr: errRefused}
	ledger, err := New(backend).NewLedger(testRetention(), fixedClock())
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}
	ledger.Record(RequestRecord{RequestID: "request-1"})
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if stats := ledger.Stats(); stats.Failed != 1 || stats.Written != 0 {
		t.Fatalf("Stats() = %+v, want Failed 1 and Written 0", stats)
	}
}

// Test flow:
//  1. Record a row under a fixed clock with a 24-hour age bound and a 100-row bound, then close the ledger.
//  2. Assert the final sweep deleted by the cutoff 24 hours before the clock and kept exactly 100 rows.
//  3. Assert the stored row carries the clock as its recorded time.
func TestTheSweepUsesBothRetentionBounds(t *testing.T) {
	backend := &fakeBackend{}
	clock := fixedClock()
	ledger, err := New(backend).NewLedger(testRetention(), clock)
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}
	ledger.Record(RequestRecord{RequestID: "request-1"})
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	wantCutoff := clock().Add(-24 * time.Hour)
	if len(backend.deleteBefore) == 0 || !backend.deleteBefore[len(backend.deleteBefore)-1].Equal(wantCutoff) {
		t.Fatalf("DeleteRequestsBefore cutoffs = %v, want the last to be %v", backend.deleteBefore, wantCutoff)
	}
	if len(backend.deleteBeyond) == 0 || backend.deleteBeyond[len(backend.deleteBeyond)-1] != 100 {
		t.Fatalf("DeleteRequestsBeyond keeps = %v, want the last to be 100", backend.deleteBeyond)
	}
	if len(backend.inserted) != 1 || !backend.inserted[0].RecordedAt.Equal(clock()) {
		t.Fatalf("inserted = %+v, want one row recorded at the clock", backend.inserted)
	}
}

// Test flow:
//  1. Make each retention bound's delete fail in turn, then both, and close a ledger over each.
//  2. Assert the sweep failures are counted one per failed bound and the other bound still ran.
func TestAFailedSweepIsCountedPerBound(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		beforeErr  error
		beyondErr  error
		wantFailed int64
	}{
		{"age bound only", errRefused, nil, 1},
		{"row bound only", nil, errRefused, 1},
		{"both bounds", errRefused, errRefused, 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backend := &fakeBackend{sweepBeforeErr: testCase.beforeErr, sweepBeyondErr: testCase.beyondErr}
			ledger, err := New(backend).NewLedger(testRetention(), fixedClock())
			if err != nil {
				t.Fatalf("NewLedger() = %v, want nil", err)
			}
			if err := ledger.Close(); err != nil {
				t.Fatalf("Close() = %v, want nil", err)
			}
			if got := ledger.Stats().SweepFailed; got != testCase.wantFailed {
				t.Fatalf("Stats().SweepFailed = %d, want %d", got, testCase.wantFailed)
			}
			if len(backend.deleteBefore) == 0 || len(backend.deleteBeyond) == 0 {
				t.Fatalf("sweeps ran %d and %d times, want both bounds to run whatever the other did", len(backend.deleteBefore), len(backend.deleteBeyond))
			}
		})
	}
}

// Test flow:
//  1. Ask for ledgers with no clock, a zero max age and a zero max rows.
//  2. Assert each is refused.
func TestNewLedgerRejectsAnUnboundedOrClocklessLedger(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		retention Retention
		now       func() time.Time
	}{
		{"no clock", testRetention(), nil},
		{"zero max age", Retention{MaxRows: 1}, fixedClock()},
		{"zero max rows", Retention{MaxAge: time.Hour}, fixedClock()},
	} {
		if _, err := New(&fakeBackend{}).NewLedger(testCase.retention, testCase.now); err == nil {
			t.Fatalf("%s: NewLedger() = nil, want a refusal", testCase.name)
		}
	}
}

// Test flow:
//  1. Open a ledger and record three rows, then close the store without closing the ledger.
//  2. Assert the three inserts reached the backend before the backend closed.
func TestStoreCloseDrainsTheLedgerBeforeTheBackendCloses(t *testing.T) {
	backend := &fakeBackend{}
	composed := New(backend)
	ledger, err := composed.NewLedger(testRetention(), fixedClock())
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}
	for _, requestID := range []string{"a", "b", "c"} {
		ledger.Record(RequestRecord{RequestID: requestID})
	}
	if err := composed.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	events := backend.recorded()
	if want := []string{"insert", "insert", "insert", "close"}; !slices.Equal(events, want) {
		t.Fatalf("backend events = %v, want %v", events, want)
	}
}

// Test flow:
//  1. Close a ledger, then record a row into it.
//  2. Assert the row is counted as dropped and nothing panics.
func TestRecordAfterCloseIsCountedNotPanicked(t *testing.T) {
	ledger, err := New(&fakeBackend{}).NewLedger(testRetention(), fixedClock())
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	ledger.Record(RequestRecord{RequestID: "late"})
	if got := ledger.Stats().Dropped; got != 1 {
		t.Fatalf("Stats().Dropped = %d, want 1", got)
	}
}

// Test flow:
//  1. Open a ledger and ask for a second one on the same store.
//  2. Assert the second is refused while the first is open and allowed once it is closed.
func TestASecondLedgerIsRefusedWhileTheFirstIsOpen(t *testing.T) {
	composed := New(&fakeBackend{})
	first, err := composed.NewLedger(testRetention(), fixedClock())
	if err != nil {
		t.Fatalf("NewLedger() = %v, want nil", err)
	}
	if _, err := composed.NewLedger(testRetention(), fixedClock()); err == nil {
		t.Fatal("second NewLedger() = nil, want a refusal")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	second, err := composed.NewLedger(testRetention(), fixedClock())
	if err != nil {
		t.Fatalf("NewLedger() after close = %v, want nil", err)
	}
	if err := errors.Join(second.Close(), composed.Close()); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
}
