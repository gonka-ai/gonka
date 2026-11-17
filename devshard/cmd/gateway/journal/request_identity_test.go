package journal

import (
	"testing"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/scheduler"
)

// Test flow:
//  1. Build a journal with a `logcapture.Recorder`.
//  2. Record a `GhostBurned` event that carries a `RequestID`.
//  3. Flush the journal.
//  4. Assert the logged "nonce burned for nobody" line names the request the burn was spent during.
func TestABurnNamesTheRequestItWasSpentDuring(t *testing.T) {
	lines := &logcapture.Recorder{}
	events := newJournal(t, Settings{Lines: lines})

	events.GhostBurned("escrow-1", scheduler.Burn{
		Nonce: 42, Participant: hostAlpha, Reason: scheduler.GhostReasonAbandoned, RequestID: "request-7",
	})
	events.Flush()

	lines.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "nonce burned for nobody", Fields: []any{
		"escrow", "escrow-1", "nonce", uint64(42), "host", "aaaaaaaa", "reason", scheduler.GhostReasonAbandoned,
		"burned_during_request", "request-7",
	}})
}

// Test flow:
//  1. Build a journal with a `logcapture.Recorder`.
//  2. Record a failed timeout vote via `RecordTimeout` that carries a `RequestID`.
//  3. Flush the journal.
//  4. Assert the logged "timeout vote failed" line names the request.
func TestAFailedTimeoutVoteNamesItsRequest(t *testing.T) {
	lines := &logcapture.Recorder{}
	events := newJournal(t, Settings{Lines: lines})

	events.RecordTimeout(engine.TimeoutEvent{
		RequestID: "req-9", EscrowID: "7", Participant: hostAlpha, Model: "qwen", Nonce: 42,
		Kind: engine.TimeoutKindExecution, Action: engine.TimeoutActionFailed, Reason: engine.TimeoutReasonNotApplied,
	})
	events.Flush()

	lines.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "timeout vote failed", Fields: []any{
		"request", "req-9", "escrow", "7", "nonce", uint64(42), "host", "aaaaaaaa", "model", "qwen",
		"kind", "execution", "reason", "timeout_not_applied",
	}})
}
