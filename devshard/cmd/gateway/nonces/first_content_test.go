package nonces

import (
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
)

// Test flow:
//  1. Record a race whose losing attempt reached first content 250 ms after its send, and whose winning attempt sent but produced no content.
//  2. Assert the folded records carry 250 ms over 1 timed attempt.
func TestARaceReportsTheTimeToFirstContentOfEachAttemptThatProducedContent(t *testing.T) {
	t.Parallel()
	escrow := newLiveEscrow(t, 3)
	ledger := newOpenedLedger(t, escrow)
	sent := time.Unix(1, 0)

	ledger.RecordRace(engine.RaceOutcome{
		RequestID: "request-1",
		EscrowID:  tokenEscrowID,
		Model:     "llama",
		Attempts: []engine.AttemptOutcome{
			{Nonce: 1, SendTime: sent, FirstContent: sent.Add(250 * time.Millisecond), Terminal: engine.TerminalLost},
			{Nonce: 2, SendTime: sent, Terminal: engine.TerminalWon},
		},
	})

	var milliseconds, attempts uint64
	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		milliseconds += record.FirstContentMillisecondsTotal
		attempts += record.FirstContentAttempts
	}
	if milliseconds != 250 || attempts != 1 {
		t.Fatalf("first content = %d ms over %d attempts, want 250 ms over the 1 attempt that produced content",
			milliseconds, attempts)
	}
}
