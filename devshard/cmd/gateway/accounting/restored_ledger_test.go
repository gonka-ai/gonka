package accounting

import (
	"reflect"
	"slices"
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Open a book, observe the latest nonce, save and reload it.
//  2. On the restored book, record a validation, an applied timeout and a challenged inference.
//  3. Assert none of those recording paths return an error.
func TestARestoredEscrowAcceptsEveryRecordingPath(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 4); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	restored := saveAndReload(t, book, openTestStore(t))

	if err := restored.RecordValidation(testEscrow, 1); err != nil {
		t.Fatalf("RecordValidation(): %v", err)
	}
	if err := restored.RecordAppliedTimeout(testEscrow, 2); err != nil {
		t.Fatalf("RecordAppliedTimeout(): %v", err)
	}
	if err := restored.ObserveInferences(testEscrow, challengedNonces(1, 3)); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}
}

// Test flow:
//  1. Build an `escrowLedger` through `newEscrowLedger`.
//  2. Walk every field of the struct by reflection.
//  3. Assert no map field is left nil.
func TestEveryMapOfAnEscrowLedgerIsBuilt(t *testing.T) {
	ledger := reflect.ValueOf(newEscrowLedger(EscrowMetadata{EscrowID: "e1"})).Elem()
	for i := range ledger.NumField() {
		field := ledger.Field(i)
		if field.Kind() == reflect.Map && field.IsNil() {
			t.Errorf("escrowLedger.%s is nil: the first write to it panics", ledger.Type().Field(i).Name)
		}
	}
}

// Test flow:
//  1. Observe the latest nonce as 40, apply 30 timeouts, and observe host stats reporting 30 misses.
//  2. Assert no chain-disagreement finding before any restart.
//  3. Save and reload the book.
//  4. Assert the restored record still raises no chain-disagreement finding.
func TestARestartDoesNotInventADisagreementWithTheChain(t *testing.T) {
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
	if codes := codesOf(findingsFor(book.Query(QueryFilter{})[0])); slices.Contains(codes, FindingChainDisagreement) {
		t.Fatalf("findings %v disagree with the chain before any restart", codes)
	}

	restored := saveAndReload(t, book, openTestStore(t))

	records := restored.Query(QueryFilter{})
	if len(records) != 1 {
		t.Fatalf("Query() returned %d records, want the one participant", len(records))
	}
	if codes := codesOf(findingsFor(records[0])); slices.Contains(codes, FindingChainDisagreement) {
		t.Errorf("findings %v report a disagreement the restart invented", codes)
	}
}
