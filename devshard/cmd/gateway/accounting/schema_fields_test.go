package accounting

import (
	"testing"

	"devshard/types"
)

func schemaTestBook(t *testing.T) *Book {
	t.Helper()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: "e1", CreationEpoch: 9, Model: "m",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "p0"}, {SlotID: 1, ValidatorAddress: "p1"}},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	return book
}

func recordFor(t *testing.T, book *Book, participant string) ParticipantRecord {
	t.Helper()
	for _, record := range book.Query(QueryFilter{EpochIndex: 9}) {
		if record.Participant == participant {
			return record
		}
	}
	t.Fatalf("no record for %s", participant)
	return ParticipantRecord{}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and record a sent, unfinished race attempt on slot 1.
//  2. Read slot 1's record.
//  3. Assert its in-flight count is 1 and its pending count is 0.
func TestInFlightIsSeparateFromPending(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.RecordRace("e1", []Attempt{{Nonce: 1, Sent: true}}); err != nil {
		t.Fatalf("RecordRace: %v", err)
	}

	record := recordFor(t, book, "p1")

	if record.InFlight != 1 {
		t.Errorf("in_flight = %d, want the sent-and-unfinished nonce", record.InFlight)
	}
	if record.Pending != 0 {
		t.Errorf("pending = %d, want the sent nonce counted as in flight instead", record.Pending)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and record a started, unsettled timeout on nonce 1.
//  2. Read slot 1's record.
//  3. Assert its timeout-pending count is 1 and its timeout outcomes are empty.
func TestTimeoutPendingCountsRoundsStillOwedAnOutcome(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.RecordTimeout("e1", 1, "refused", "started", "none"); err != nil {
		t.Fatalf("RecordTimeout: %v", err)
	}

	record := recordFor(t, book, "p1")

	if record.TimeoutPending != 1 {
		t.Errorf("timeout_pending = %d, want the round still owed an outcome", record.TimeoutPending)
	}
	if len(record.TimeoutOutcomes) != 0 {
		t.Errorf("timeout_outcomes = %v, want none: the round has not settled", record.TimeoutOutcomes)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture, observe host stats reporting misses and invalids, and record a completed timeout.
//  2. Read slot 1's record.
//  3. Assert the cross-checks carry both the chain's side (host missed, host invalid) and the ledger's side (timeout applied).
func TestCrossChecksCarryBothSides(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.ObserveHostStats("e1", 1, types.HostStats{Missed: 5, Invalid: 2}); err != nil {
		t.Fatalf("ObserveHostStats: %v", err)
	}
	if err := book.RecordTimeout("e1", 1, "refused", "completed", "none"); err != nil {
		t.Fatalf("RecordTimeout: %v", err)
	}

	record := recordFor(t, book, "p1")

	if record.CrossChecks.HostMissed != 5 || record.CrossChecks.HostInvalid != 2 {
		t.Errorf("chain side = %d/%d, want 5/2", record.CrossChecks.HostMissed, record.CrossChecks.HostInvalid)
	}
	if record.CrossChecks.TimeoutApplied != 1 {
		t.Errorf("ledger side = %d, want the one applied timeout", record.CrossChecks.TimeoutApplied)
	}
}

// Test flow:
//  1. Open an escrow where one participant holds two slots, then observe the chain's latest nonce.
//  2. Read that participant's record.
//  3. Assert its latest-nonces list holds exactly one entry, naming the escrow and its latest nonce.
func TestLatestNoncesListEachEscrowOnce(t *testing.T) {
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: "e1", CreationEpoch: 9, Model: "m",
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "p0"},
			{SlotID: 1, ValidatorAddress: "p0"},
			{SlotID: 2, ValidatorAddress: "p1"},
		},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	if err := book.ObserveLatestNonce("e1", 42); err != nil {
		t.Fatalf("ObserveLatestNonce: %v", err)
	}

	record := recordFor(t, book, "p0")

	if len(record.LatestNonces) != 1 {
		t.Fatalf("latest_nonces = %v, want one entry for the one escrow", record.LatestNonces)
	}
	if got := record.LatestNonces[0]; got.EscrowID != "e1" || got.LatestNonce != 42 {
		t.Errorf("latest_nonces[0] = %+v, want e1 at 42", got)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture, observe the latest nonce, then retire the escrow.
//  2. Read slot 1's record.
//  3. Assert its one latest-nonces entry is marked retired.
func TestLatestNoncesMarkARetiredEscrow(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.ObserveLatestNonce("e1", 7); err != nil {
		t.Fatalf("ObserveLatestNonce: %v", err)
	}
	book.RetireEscrow("e1")

	record := recordFor(t, book, "p1")

	if len(record.LatestNonces) != 1 || !record.LatestNonces[0].Retired {
		t.Errorf("latest_nonces = %+v, want the escrow marked retired", record.LatestNonces)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and observe 3 challenged nonces on slot 1.
//  2. Assert slot 1's unresolved-challenges count is 3 and slot 0's is 0.
//  3. Observe the challenge count dropping to 1 on slot 1.
//  4. Assert slot 1's unresolved-challenges count reads 1, a gauge rather than a running total.
func TestUnresolvedChallengesAreAGaugePerSlot(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.ObserveInferences("e1", challengedNonces(1, 3)); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}

	if got := recordFor(t, book, "p1").UnresolvedChallenges; got != 3 {
		t.Errorf("unresolved_challenges = %d, want 3", got)
	}
	if got := recordFor(t, book, "p0").UnresolvedChallenges; got != 0 {
		t.Errorf("the other slot carries %d, want none", got)
	}

	if err := book.ObserveInferences("e1", challengedNonces(1, 1)); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}
	if got := recordFor(t, book, "p1").UnresolvedChallenges; got != 1 {
		t.Errorf("unresolved_challenges = %d after two resolved, want 1: this is a gauge, not a total", got)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and record two validations from slot 0.
//  2. Assert slot 0's validations-performed count is 2.
//  3. Assert slot 1, the slot that was checked, carries no validations-performed credit.
func TestValidationsCreditTheSlotThatChecked(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.RecordValidation("e1", 0); err != nil {
		t.Fatalf("RecordValidation: %v", err)
	}
	if err := book.RecordValidation("e1", 0); err != nil {
		t.Fatalf("RecordValidation: %v", err)
	}

	if got := recordFor(t, book, "p0").ValidationsPerformed; got != 2 {
		t.Errorf("validations_performed = %d, want 2", got)
	}
	if got := recordFor(t, book, "p1").ValidationsPerformed; got != 0 {
		t.Errorf("the checked slot was credited %d, want none", got)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and record a validation from slot 9, outside the two-slot group.
//  2. Assert the call returns an error.
func TestValidationsRejectASlotOutsideTheGroup(t *testing.T) {
	book := schemaTestBook(t)

	if err := book.RecordValidation("e1", 9); err == nil {
		t.Fatal("a slot outside the group was accepted")
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and record an applied timeout on nonce 3, whose executor is slot 1.
//  2. Assert slot 1's timeouts-applied count is 1.
//  3. Assert slot 0 was not credited.
func TestAppliedTimeoutsCreditTheExecutorSlot(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.RecordAppliedTimeout("e1", 3); err != nil {
		t.Fatalf("RecordAppliedTimeout: %v", err)
	}

	if got := recordFor(t, book, "p1").TimeoutsApplied; got != 1 {
		t.Errorf("timeouts_applied = %d for slot 1, want 1", got)
	}
	if got := recordFor(t, book, "p0").TimeoutsApplied; got != 0 {
		t.Errorf("slot 0 was credited %d, want none", got)
	}
}

// Test flow:
//  1. Open an escrow where one participant, "both", holds two slots.
//  2. Observe host stats on both slots, but put 4 misses only on slot 0, then apply 4 timeouts, which credit slot 1.
//  3. Assert the participant's totals agree, 4 timeouts applied and 4 host misses.
//  4. Assert the cross-check error count is 8, since slot 0 is short 4 and slot 1 has 4 the chain never counted.
func TestCrossCheckErrorDoesNotLetSlotsCancelEachOther(t *testing.T) {
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: "e1", CreationEpoch: 9, Model: "m",
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "both"},
			{SlotID: 1, ValidatorAddress: "both"},
		},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	for slotID := range uint32(2) {
		if err := book.ObserveHostStats("e1", slotID, types.HostStats{}); err != nil {
			t.Fatalf("ObserveHostStats: %v", err)
		}
	}
	if err := book.ObserveHostStats("e1", 0, types.HostStats{Missed: 4}); err != nil {
		t.Fatalf("ObserveHostStats: %v", err)
	}
	for _, nonce := range []uint64{1, 3, 5, 7} {
		if err := book.RecordAppliedTimeout("e1", nonce); err != nil {
			t.Fatalf("RecordAppliedTimeout: %v", err)
		}
	}

	record := recordFor(t, book, "both")

	if record.TimeoutsApplied != 4 || record.CrossChecks.HostMissed != 4 {
		t.Fatalf("sides = %d ledger / %d chain, want 4/4 so the totals agree while the slots do not",
			record.TimeoutsApplied, record.CrossChecks.HostMissed)
	}
	if record.CrossChecks.ErrorCount != 8 {
		t.Errorf("error_count = %d, want 8: slot 0 is short four and slot 1 has four the chain never counted",
			record.CrossChecks.ErrorCount)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture, record a validation from slot 0, then record an invalid verdict against nonce 3, executed by slot 1.
//  2. Assert slot 1, the executor, is charged 1 recorded-invalid.
//  3. Assert slot 0, the validator that rejected it, carries none.
func TestARejectedAnswerChargesTheExecutorNotTheValidator(t *testing.T) {
	book := schemaTestBook(t)
	const executedBySlotOne = uint64(3)

	if err := book.RecordValidation("e1", 0); err != nil {
		t.Fatalf("RecordValidation: %v", err)
	}
	if err := book.RecordInvalidVerdict("e1", executedBySlotOne); err != nil {
		t.Fatalf("RecordInvalidVerdict: %v", err)
	}

	if got := recordFor(t, book, "p1").CrossChecks.RecordedInvalid; got != 1 {
		t.Errorf("recorded_invalid_transitions = %d for the executor, want 1", got)
	}
	if got := recordFor(t, book, "p0").CrossChecks.RecordedInvalid; got != 0 {
		t.Errorf("the validator that rejected was charged %d, want none", got)
	}
}

// Test flow:
//  1. Build a `schemaTestBook` fixture and record a started, unsettled timeout on nonce 1.
//  2. Read slot 1's record and its one escrow row.
//  3. Assert the escrow row's timeout-pending count is 1.
//  4. Assert the host row's timeout-pending count matches the same 1.
func TestASlotCarriesTheTimeoutsOfItsOwnEscrow(t *testing.T) {
	book := schemaTestBook(t)
	if err := book.RecordTimeout("e1", 1, "refused", "started", "none"); err != nil {
		t.Fatalf("RecordTimeout: %v", err)
	}

	record := recordFor(t, book, "p1")
	if len(record.Slots) != 1 {
		t.Fatalf("the host holds %d escrow rows, want 1", len(record.Slots))
	}
	if got := record.Slots[0].TimeoutPending; got != 1 {
		t.Errorf("the escrow row reports timeout_pending = %d, want 1", got)
	}
	if got := record.TimeoutPending; got != 1 {
		t.Errorf("the host row reports timeout_pending = %d, want the same 1", got)
	}
}
