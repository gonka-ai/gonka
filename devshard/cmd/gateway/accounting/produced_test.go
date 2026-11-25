package accounting

import (
	"testing"

	"devshard/types"
)

func outputOf(t *testing.T, book *Book) uint64 {
	t.Helper()
	var output uint64
	for _, record := range book.Query(QueryFilter{}) {
		output += record.OutputTokens
	}
	return output
}

// Test flow:
//  1. Observe a finished inference reporting 0 output tokens from the host.
//  2. Record the attempt as produced with 834 output tokens, the gateway's own count off the stream.
//  3. Assert the summed output tokens equal 834, not the host's claim.
func TestTheAnswerIsCountedByTheGatewayNotByTheHostsClaim(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		5: {Status: types.StatusFinished, ExecutorSlot: 1, InputLength: 400, InputTokens: 39642, OutputTokens: 0},
	}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	recordProduced(t, book, testEscrow, 5, 834)

	if got := outputOf(t, book); got != 834 {
		t.Fatalf("output_tokens = %d, want the 834 the gateway counted off the stream", got)
	}
}

// Test flow:
//  1. Record a race of two attempts under one request: one losing with 834 output tokens, one winning with 4096.
//  2. Assert the summed output tokens equal 4930, both attempts counted.
func TestALostAttemptStillReportsWhatItProduced(t *testing.T) {
	book := newTestBook(t, 4)

	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 5, RequestID: "request-1", Sent: true, Usage: UsageLoser, OutputTokens: 834},
		{Nonce: 6, RequestID: "request-1", Sent: true, Usage: UsageWinner, OutputTokens: 4096},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 5, 6)

	if got := outputOf(t, book); got != 4_930 {
		t.Fatalf("output_tokens = %d, want both attempts of the race counted", got)
	}
}

// Test flow:
//  1. Record the same attempt as produced with 834 output tokens three times.
//  2. Assert the summed output tokens stay 834, not tripled.
func TestAnAttemptReportedTwiceIsCountedOnce(t *testing.T) {
	book := newTestBook(t, 4)

	for range 3 {
		recordProduced(t, book, testEscrow, 5, 834)
	}

	if got := outputOf(t, book); got != 834 {
		t.Fatalf("output_tokens = %d, want 834 after the same attempt was reported three times", got)
	}
}

// Test flow:
//  1. Record two attempts as produced on slots 1 and 2, with 834 and 4096 output tokens.
//  2. Save and reload the book.
//  3. Assert the restored summed output tokens equal 4930.
//  4. Assert slot 1 and slot 2's records each carry their own output-token count.
func TestWhatTheGatewayCountedSurvivesARestart(t *testing.T) {
	book := newTestBook(t, 4)
	recordProduced(t, book, testEscrow, 5, 834)
	recordProduced(t, book, testEscrow, 6, 4096)

	restored := saveAndReload(t, book, openTestStore(t))

	if got := outputOf(t, restored); got != 4_930 {
		t.Fatalf("output_tokens = %d after the restart, want the 4930 the gateway had counted", got)
	}
	for _, record := range restored.Query(QueryFilter{}) {
		if record.Participant == participantFor(1) && record.OutputTokens != 834 {
			t.Fatalf("slot 1 came back with %d output tokens, want the 834 it produced", record.OutputTokens)
		}
		if record.Participant == participantFor(2) && record.OutputTokens != 4_096 {
			t.Fatalf("slot 2 came back with %d output tokens, want the 4096 it produced", record.OutputTokens)
		}
	}
}

// Test flow:
//  1. Record an attempt as produced with 834 output tokens.
//  2. Observe the same nonce's inference from the chain three times, with no output tokens reported.
//  3. Assert the summed output tokens stay at 834, the gateway's own count.
func TestAChainReadingDoesNotDisturbWhatTheGatewayCounted(t *testing.T) {
	book := newTestBook(t, 4)
	recordProduced(t, book, testEscrow, 5, 834)

	for range 3 {
		if err := book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
			5: {Status: types.StatusFinished, ExecutorSlot: 1, InputLength: 400, InputTokens: 39642},
		}); err != nil {
			t.Fatalf("ObserveInferences(): %v", err)
		}
	}

	if got := outputOf(t, book); got != 834 {
		t.Fatalf("output_tokens = %d after three sweeps, want the 834 the gateway counted", got)
	}
}
