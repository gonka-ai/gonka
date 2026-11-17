package accounting

import (
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Open an escrow with four slot assignments and observe the chain's latest nonce.
//  2. Assign three nonces, two of them racing the same request.
//  3. Assert the epoch summary reports 3 nonces in flight.
//  4. Assert it reports 2 in-flight requests, since one client is racing two hosts.
func TestTheEpochSummaryCarriesWhatIsInFlight(t *testing.T) {
	book := NewBook(nil)
	slots := make([]types.SlotAssignment, 0, 4)
	for slotID := range uint32(4) {
		slots = append(slots, types.SlotAssignment{SlotID: slotID, ValidatorAddress: participantFor(int(slotID))})
	}
	if err := book.OpenEscrow(EscrowMetadata{EscrowID: "e1", CreationEpoch: 9, Model: "m", Slots: slots}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}
	if err := book.ObserveLatestNonce("e1", 8); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	for nonce, requestID := range map[uint64]string{1: "a", 2: "a", 3: "b"} {
		if err := book.RecordAssigned("e1", nonce, requestID); err != nil {
			t.Fatalf("RecordAssigned(%d): %v", nonce, err)
		}
	}

	summaries := book.Epochs(QueryFilter{})

	if len(summaries) != 1 {
		t.Fatalf("got %d epoch summaries, want one", len(summaries))
	}
	if summaries[0].InFlight != 3 {
		t.Fatalf("in_flight = %d, want the 3 nonces out with hosts", summaries[0].InFlight)
	}
	if summaries[0].InFlightRequests != 2 {
		t.Fatalf("in_flight_requests = %d, want the 2 clients waiting: one of them is racing two hosts",
			summaries[0].InFlightRequests)
	}
}

// Test flow:
//  1. Open a book and observe the chain's latest nonce.
//  2. Observe three inferences: one finished, one timed out, one still started.
//  3. Sum the nonce totals across the queried records.
//  4. Assert estimated input tokens come only from the finished nonce's bytes.
//  5. Assert estimated error tokens sum the bytes of the two nonces that never came back.
//  6. Assert counted nonces is 1, the one the chain counted tokens for.
func TestTheEstimateSeparatesWhatFinishedFromWhatDidNot(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 12); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}

	if err := book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusFinished, InputLength: 4_000, MaxTokens: 256, InputTokens: 900, OutputTokens: 100},
		2: {Status: types.StatusTimedOut, InputLength: 8_000, MaxTokens: 256},
		3: {Status: types.StatusStarted, InputLength: 12_000, MaxTokens: 256},
	}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	var totals nonceTotals
	for _, record := range book.Query(QueryFilter{}) {
		totals.add(record.nonceTotals)
	}

	if totals.EstimatedInput != 1_000 {
		t.Fatalf("estimated_input_tokens = %d, want the finished nonce's 4000 bytes", totals.EstimatedInput)
	}
	if totals.EstimatedError != 5_000 {
		t.Fatalf("estimated_error_tokens = %d, want the 8000 and 12000 bytes that never came back", totals.EstimatedError)
	}
	if totals.CountedNonces != 1 {
		t.Fatalf("counted_nonces = %d, want the one the chain counted tokens for", totals.CountedNonces)
	}
}
