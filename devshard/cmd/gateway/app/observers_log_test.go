package app

import (
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/scheduler"
)

// Test flow:
//  1. Build a tracedDispatches over a dispatch recorder and a journal.
//  2. Call GhostBurned for one escrow and burn.
//  3. Flush the journal.
//  4. Assert the log carries a "nonce burned for nobody" line naming the escrow, nonce, host and reason.
func TestABurnedNonceIsLoggedWithTheEscrowItCostAndWhy(t *testing.T) {
	logged := logcapture.Install(t)
	events := newTestJournal(t)
	dispatches := tracedDispatches{recorder: metrics.NewDispatchRecorder(metrics.New()), events: events}

	dispatches.GhostBurned("escrow-1", scheduler.Burn{
		Nonce: 5, Participant: "gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Reason: "participant_window_full_no_send",
	})
	events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "nonce burned for nobody", Fields: []any{
		"escrow", "escrow-1", "nonce", uint64(5), "host", "bbbbbbbb", "reason", "participant_window_full_no_send",
	}})
}

// Test flow:
//  1. Build a tracedDispatches over a dispatch recorder and a journal.
//  2. Call BurnBudgetExhausted for one escrow.
//  3. Flush the journal.
//  4. Assert the log carries an "escrow stopped burning nonces at its budget" line naming the escrow.
func TestAnEscrowAtItsBurnBudgetSaysSo(t *testing.T) {
	logged := logcapture.Install(t)
	events := newTestJournal(t)
	dispatches := tracedDispatches{recorder: metrics.NewDispatchRecorder(metrics.New()), events: events}

	dispatches.BurnBudgetExhausted("escrow-1")
	events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "escrow stopped burning nonces at its budget", Fields: []any{
		"escrow", "escrow-1",
	}})
}

// Test flow:
//  1. Build a tracedDispatches over a dispatch recorder and a journal.
//  2. Call ExcludedHostServed for one escrow and host.
//  3. Flush the journal.
//  4. Assert the log carries a "nonce spent on a host the request excluded" line naming the escrow and host.
func TestANonceSpentOnAnExcludedHostNamesItsEscrowAndHost(t *testing.T) {
	logged := logcapture.Install(t)
	events := newTestJournal(t)
	dispatches := tracedDispatches{recorder: metrics.NewDispatchRecorder(metrics.New()), events: events}

	dispatches.ExcludedHostServed("escrow-1", "gonka1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "info", Msg: "nonce spent on a host the request excluded", Fields: []any{
		"escrow", "escrow-1", "host", "bbbbbbbb",
	}})
}

// Test flow:
//  1. Build a race recorder and a failed timeout event.
//  2. Run the event through nonceAccountedRaces.RecordTimeout and flush the journal.
//  3. Assert the log carries a "timeout vote failed" line.
//  4. Run the same event through probeVotes.RecordTimeout and flush again.
//  5. Assert the log still holds exactly one line, since a warmup vote reaches RecordProbeTimeout rather than writing the race vote's own line again.
func TestAFailedWarmupVoteWritesNoTimeoutVoteLine(t *testing.T) {
	logged := logcapture.Install(t)
	events := newTestJournal(t)
	recorder := metrics.NewRaceRecorder(metrics.New(), time.Now, func() time.Duration { return time.Hour })
	failedVote := engine.TimeoutEvent{
		EscrowID: "escrow-1", Participant: "gonka1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Model: "test-model",
		Nonce: 5, Kind: engine.TimeoutKindRefused, Action: engine.TimeoutActionFailed, Reason: "some_other_reason",
	}

	races := nonceAccountedRaces{recorder: recorder, events: events}
	races.RecordTimeout(failedVote)
	events.Flush()
	if _, found := logged.Find("timeout vote failed"); !found {
		t.Fatal("a failed race vote wrote no line -- this test cannot tell a written line from a missing one")
	}

	probes := probeVotes{recorder: recorder, events: events}
	probes.RecordTimeout(failedVote)
	events.Flush()

	if lines := logged.All(); len(lines) != 1 {
		t.Fatalf("lines after the warmup's own failed vote = %d, want 1: the warmup vote must reach RecordProbeTimeout, not the race's own line", len(lines))
	}
}

// Test flow:
//  1. Build a phaseNarrator over a journal.
//  2. Observe an error-only snapshot, then one that opens a PoC block, then one that clears it.
//  3. Flush the journal.
//  4. Assert the log carries a "chain epoch" line, a "chain blocked requests" line, and a "chain unblocked requests" line with the expected fields.
//  5. Assert exactly three lines were recorded, since the epoch-less first snapshot announces nothing.
func TestThePhaseNarratorHandsItsChangesToTheJournal(t *testing.T) {
	logged := logcapture.Install(t)
	events := newTestJournal(t)
	narrator := &phaseNarrator{events: events}

	narrator.observe(chain.PhaseSnapshot{LastError: "fetch epoch info: 503"})
	narrator.observe(chain.PhaseSnapshot{EpochIndex: 7, EpochPhase: chain.EpochPhasePoCGenerate, BlockHeight: 100, RequestsBlocked: true, BlockReason: chain.BlockReasonPoC})
	narrator.observe(chain.PhaseSnapshot{EpochIndex: 7, EpochPhase: chain.EpochPhasePoCGenerate, BlockHeight: 120})
	events.Flush()

	logged.RequireLine(t, logcapture.Entry{Level: "info", Msg: "chain epoch", Fields: []any{
		"epoch", uint64(7), "phase", chain.EpochPhasePoCGenerate, "height", int64(100), "switch_height", int64(0),
	}})
	logged.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "chain blocked requests", Fields: []any{
		"reason", chain.BlockReasonPoC, "epoch", uint64(7), "height", int64(100),
	}})
	logged.RequireLine(t, logcapture.Entry{Level: "info", Msg: "chain unblocked requests", Fields: []any{
		"epoch", uint64(7), "height", int64(120),
	}})
	if recorded := logged.All(); len(recorded) != 3 {
		t.Fatalf("recorded %+v, want exactly three chain lines: the epoch-less first snapshot announces no epoch", recorded)
	}
}
