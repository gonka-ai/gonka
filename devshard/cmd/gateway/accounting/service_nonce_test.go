package accounting

import (
	"maps"
	"testing"
)

func recordServiceNonce(t *testing.T, book *Book, nonce uint64, purpose ServicePurpose) {
	t.Helper()
	if err := book.RecordServiceNonce("e1", nonce, purpose); err != nil {
		t.Fatalf("RecordServiceNonce(%d, %s): %v", nonce, purpose, err)
	}
}

func observeCharging(t *testing.T, book *Book, feePerNonce, finalizeNonce uint64) {
	t.Helper()
	if err := book.ObserveCharging("e1", feePerNonce, finalizeNonce); err != nil {
		t.Fatalf("ObserveCharging: %v", err)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture (two slots), observe the latest nonce as 6 and a fee of 10 per nonce.
//  2. Record a heartbeat and a timeout round on slot 1's nonces 1 and 3.
//  3. Assert slot 1 reports both by purpose, pays 20 for them, and only its nonce 5 stays unclassified.
func TestServiceNoncesAreCountedByPurposeApartFromUnclassified(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.ObserveLatestNonce("e1", 6); err != nil {
		t.Fatalf("ObserveLatestNonce: %v", err)
	}
	observeCharging(t, book, 10, 0)
	recordServiceNonce(t, book, 1, ServiceHeartbeat)
	recordServiceNonce(t, book, 3, ServiceTimeout)

	record := recordFor(t, book, "p1")

	want := map[ServicePurpose]uint64{ServiceHeartbeat: 1, ServiceTimeout: 1}
	if !maps.Equal(record.ServiceNonces, want) || record.ServiceFee != 20 {
		t.Errorf("service_nonces/service_fee = %v/%d, want %v/20", record.ServiceNonces, record.ServiceFee, want)
	}
	if record.Assigned != 3 || record.Unobserved != 1 {
		t.Errorf("assigned/unclassified = %d/%d, want 3/1", record.Assigned, record.Unobserved)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture with a fee of 10 per nonce.
//  2. Record a flush on nonce 1, the finalize round on nonce 3, and a plain round after it on nonce 5.
//  3. Assert the round after the finalize is filed under finalize too, and neither finalize nonce pays the fee.
func TestEveryServiceNonceFromTheFinalizeRoundOnIsFinalizeAndFree(t *testing.T) {
	book := schemaTestBook(t)
	observeCharging(t, book, 10, 0)
	recordServiceNonce(t, book, 1, ServiceFlush)
	recordServiceNonce(t, book, 3, ServiceFinalize)
	recordServiceNonce(t, book, 5, ServiceFlush)

	record := recordFor(t, book, "p1")

	want := map[ServicePurpose]uint64{ServiceFlush: 1, ServiceFinalize: 2}
	if !maps.Equal(record.ServiceNonces, want) || record.ServiceFee != 10 {
		t.Errorf("service_nonces/service_fee = %v/%d, want %v/10", record.ServiceNonces, record.ServiceFee, want)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture with no latest nonce observed yet.
//  2. Record a heartbeat on nonce 5.
//  3. Assert slot 1's assigned range already covers it, so nothing reads as overclassified.
func TestAServiceNonceRaisesTheAssignedWatermark(t *testing.T) {
	book := schemaTestBook(t)
	recordServiceNonce(t, book, 5, ServiceHeartbeat)

	record := recordFor(t, book, "p1")

	if record.Assigned != 3 || record.Overcounted != 0 {
		t.Errorf("assigned/overclassified = %d/%d, want 3/0", record.Assigned, record.Overcounted)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture with a fee of 10 per nonce and record a heartbeat on nonce 1.
//  2. Save and reload the book.
//  3. Assert the restored slot 1 still reports the heartbeat and its fee.
func TestServiceNoncesAndTheirFeeSurviveARestart(t *testing.T) {
	book := schemaTestBook(t)
	observeCharging(t, book, 10, 0)
	recordServiceNonce(t, book, 1, ServiceHeartbeat)

	record := recordFor(t, saveAndReload(t, book, openTestStore(t)), "p1")

	want := map[ServicePurpose]uint64{ServiceHeartbeat: 1}
	if !maps.Equal(record.ServiceNonces, want) || record.ServiceFee != 10 {
		t.Errorf("restored service_nonces/service_fee = %v/%d, want %v/10", record.ServiceNonces, record.ServiceFee, want)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and observe the chain's finalize nonce as 3, as if the finalize round's own fact was lost.
//  2. Record a plain round on nonce 5.
//  3. Assert it is filed under finalize and pays no fee.
func TestTheChainsFinalizeNonceFilesLaterRoundsAsFinalize(t *testing.T) {
	book := schemaTestBook(t)
	observeCharging(t, book, 10, 3)
	recordServiceNonce(t, book, 5, ServiceFlush)

	record := recordFor(t, book, "p1")

	want := map[ServicePurpose]uint64{ServiceFinalize: 1}
	if !maps.Equal(record.ServiceNonces, want) || record.ServiceFee != 0 {
		t.Errorf("service_nonces/service_fee = %v/%d, want %v/0", record.ServiceNonces, record.ServiceFee, want)
	}
}
