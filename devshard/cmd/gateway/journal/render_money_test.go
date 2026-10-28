package journal

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logcapture"
)

// Test flow:
//  1. Install a `logcapture` recorder and build a journal.
//  2. Record a failed execution timeout vote via `RecordTimeout`.
//  3. Flush the journal.
//  4. Assert the logged "timeout vote failed" line names the nonce, host, and reason.
func TestAFailedTimeoutVoteIsLogged(t *testing.T) {
	logged := logcapture.Install(t)
	events := newJournal(t, Settings{})

	events.RecordTimeout(engine.TimeoutEvent{
		EscrowID: "7", Participant: "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Model: "qwen",
		Nonce: 42, Kind: engine.TimeoutKindExecution, Action: engine.TimeoutActionFailed, Reason: engine.TimeoutReasonNotApplied,
	})
	events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "timeout vote failed", Fields: []any{
		"escrow", "7", "nonce", uint64(42), "host", "aaaaaaaa", "model", "qwen",
		"kind", "execution", "reason", "timeout_not_applied",
	}})
}

// Test flow:
//  1. Install a `logcapture` recorder and build a journal.
//  2. Record a failed timeout vote whose reason is `TimeoutReasonEscrowGone`.
//  3. Flush the journal.
//  4. Assert nothing was logged, since the escrow's own transition line already covers it.
func TestAVoteLostWithItsEscrowIsSilent(t *testing.T) {
	logged := logcapture.Install(t)
	events := newJournal(t, Settings{})

	events.RecordTimeout(engine.TimeoutEvent{
		EscrowID: "7", Nonce: 42, Kind: engine.TimeoutKindExecution,
		Action: engine.TimeoutActionFailed, Reason: engine.TimeoutReasonEscrowGone,
	})
	events.Flush()

	require.Empty(t, logged.All(), "the escrow's own line already says this, once instead of per nonce")
}

// Test flow:
//  1. Install a `logcapture` recorder and build a journal.
//  2. Record a completed, a started, and a skipped timeout vote.
//  3. Flush the journal.
//  4. Assert nothing was logged.
func TestAPostedVoteIsSilent(t *testing.T) {
	logged := logcapture.Install(t)
	events := newJournal(t, Settings{})

	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "7", Nonce: 42, Action: engine.TimeoutActionCompleted})
	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "7", Nonce: 43, Action: engine.TimeoutActionStarted})
	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "7", Nonce: 44, Action: engine.TimeoutActionSkipped})
	events.Flush()

	require.Empty(t, logged.All())
}

// Test flow:
//  1. Install a `logcapture` recorder and build a journal with a `ledgerSpy`.
//  2. Record a failed refused-timeout probe vote via `RecordProbeTimeout`.
//  3. Flush the journal.
//  4. Assert nothing was logged but the ledger spy received the timeout fact (a warmup's vote reaches the ledger like a race's, but writes no race line).
func TestAFailedProbeVoteReachesTheLedgerWithoutARaceLine(t *testing.T) {
	logged := logcapture.Install(t)
	ledger := &ledgerSpy{}
	events := newJournal(t, Settings{Ledger: ledger})

	events.RecordProbeTimeout(engine.TimeoutEvent{
		EscrowID: "7", Model: "qwen", Nonce: 42, Kind: engine.TimeoutKindRefused,
		Action: engine.TimeoutActionFailed, Reason: engine.TimeoutReasonNotApplied,
	})
	events.Flush()

	require.Empty(t, logged.All())
	require.Equal(t, []string{"timeout 7 42 failed"}, ledger.arrived())
}

// Test flow:
//  1. Install a `logcapture` recorder and build a journal whose ledger refuses every probe with a known error.
//  2. Record a probe via `ProbeRecorded`.
//  3. Flush the journal.
//  4. Assert the logged "escrow warmup could not settle its nonce" line carries the escrow, nonce, and the ledger's refusal error.
func TestAProbeTheLedgerRefusedIsLogged(t *testing.T) {
	logged := logcapture.Install(t)
	refusal := fmt.Errorf("%w: %s", accounting.ErrUnknownEscrow, "escrow-9")
	events := newJournal(t, Settings{Ledger: &ledgerSpy{probeRefusal: refusal}})

	events.ProbeRecorded("escrow-9", accounting.Attempt{Nonce: 7, Sent: true, Acknowledged: true, Terminal: accounting.TerminalWarmupProbe})
	events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "escrow warmup could not settle its nonce", Fields: []any{
		"escrow", "escrow-9", "nonce", uint64(7), "error", fmt.Errorf("%w: %s", accounting.ErrUnknownEscrow, "escrow-9"),
	}})
}
