package accounting

import (
	"slices"
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Open a book and observe the chain's latest nonce as 40.
//  2. Observe host stats reporting 30 misses, all before the ledger started watching.
//  3. Assert the queried record raises no chain-disagreement finding.
//  4. Assert its cross-check error count is 0.
func TestAWipedLedgerDoesNotInventADisagreementWithTheChain(t *testing.T) {
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 40); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}

	if err := book.ObserveHostStats(testEscrow, 0, types.HostStats{Missed: 30}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}

	record := book.Query(QueryFilter{})[0]
	if codes := codesOf(findingsFor(record)); slices.Contains(codes, FindingChainDisagreement) {
		t.Fatalf("findings %v disagree with the chain over history the ledger never watched", codes)
	}
	if record.CrossChecks.ErrorCount != 0 {
		t.Fatalf("error_count = %d, want 0: both halves start on the same window", record.CrossChecks.ErrorCount)
	}
}

// Test flow:
//  1. Open a book and observe the chain's latest nonce as 40.
//  2. Observe host stats reporting 30 misses before the ledger watched, then 35 misses after.
//  3. Assert the cross-check error count is 5, the misses counted while the ledger was watching.
func TestADriftAfterTheLedgerStartedWatchingIsStillReported(t *testing.T) {
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 40); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.ObserveHostStats(testEscrow, 0, types.HostStats{Missed: 30}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}

	if err := book.ObserveHostStats(testEscrow, 0, types.HostStats{Missed: 35}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}

	record := book.Query(QueryFilter{})[0]
	if record.CrossChecks.ErrorCount != 5 {
		t.Fatalf("error_count = %d, want the 5 misses the chain counted while the ledger was watching",
			record.CrossChecks.ErrorCount)
	}
}

// Test flow:
//  1. Open a book and observe the chain's latest nonce as 40 with no misses.
//  2. Record an invalid verdict for four separate nonces.
//  3. Assert the record's recorded-invalid count is 4.
//  4. Assert the cross-check error count stays 0, since a validator's challenge is not the chain invalidating a nonce.
func TestADissentingValidatorIsNotADisagreementWithTheChain(t *testing.T) {
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 40); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.ObserveHostStats(testEscrow, 0, types.HostStats{}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}

	for nonce := uint64(1); nonce <= 4; nonce++ {
		if err := book.RecordInvalidVerdict(testEscrow, nonce); err != nil {
			t.Fatalf("RecordInvalidVerdict(%d): %v", nonce, err)
		}
	}

	record := book.Query(QueryFilter{})[0]
	if record.CrossChecks.RecordedInvalid != 4 {
		t.Fatalf("recorded_invalid_transitions = %d, want the 4 challenges opened", record.CrossChecks.RecordedInvalid)
	}
	if record.CrossChecks.ErrorCount != 0 {
		t.Fatalf("error_count = %d, want 0: a validator opening a challenge is not the chain invalidating a nonce",
			record.CrossChecks.ErrorCount)
	}
}

// Test flow:
//  1. Open a book, observe the latest nonce as 40, and apply 30 timeouts.
//  2. Observe host stats reporting 30 misses.
//  3. Save and reload the book, then observe the same 30 misses again on the restored book.
//  4. Assert the restored record's applied-timeouts count stays 30.
//  5. Assert the cross-check error count is 0, since a restored half is not re-seeded.
func TestARestoredLedgerKeepsItsOwnHalfRatherThanReseeding(t *testing.T) {
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 40); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	for nonce := uint64(1); nonce <= 30; nonce++ {
		if err := book.RecordAppliedTimeout(testEscrow, nonce); err != nil {
			t.Fatalf("RecordAppliedTimeout(%d): %v", nonce, err)
		}
	}
	if err := book.ObserveHostStats(testEscrow, 0, types.HostStats{Missed: 30}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}

	restored := saveAndReload(t, book, openTestStore(t))
	if err := restored.ObserveHostStats(testEscrow, 0, types.HostStats{Missed: 30}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}

	record := restored.Query(QueryFilter{})[0]
	if record.TimeoutsApplied != 30 {
		t.Fatalf("timeouts_applied = %d after a restore, want the 30 it had recorded", record.TimeoutsApplied)
	}
	if record.CrossChecks.ErrorCount != 0 {
		t.Fatalf("error_count = %d, want 0: a restored half is not re-seeded", record.CrossChecks.ErrorCount)
	}
}
