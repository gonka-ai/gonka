package engine

import (
	"testing"
	"time"

	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/scheduler"
)

// tracedCoordinator is a paused coordinator on escrow-1 that reports to events, for the Trace hooks below.
func tracedCoordinator(events raceJournal, attempts ...*liveAttempt) *raceCoordinator {
	fixture := newRaceFixture(settledPolicy(), 4)
	fixture.deps.Journal = events
	coordinator := pausedCoordinator(fixture, 4, attempts...)
	coordinator.escrowID = "escrow-1"
	return coordinator
}

// widestFinishedOutcome carries every field a finish line can report, so the line it builds is the widest one.
func widestFinishedOutcome() AttemptOutcome {
	return AttemptOutcome{
		SendTime:     testEpoch,
		ReceiptTime:  testEpoch.Add(200 * time.Millisecond),
		FirstToken:   testEpoch.Add(900 * time.Millisecond),
		FirstContent: testEpoch.Add(950 * time.Millisecond),
		Completed:    testEpoch.Add(9 * time.Second),

		ContentChunks: 412, StreamChunks: 415, OutputBytes: 61_440,
		UsageCompletionTokens: 512, HostTerminated: true,
		MaxChunkGap: 1200 * time.Millisecond, MaxChunkGapAt: 310,
		MeanChunkGap:   21 * time.Millisecond,
		UpstreamStatus: 502, UpstreamBody: "upstream is down",
	}
}

// awaitTracedAttemptDone waits for the attempt goroutine a launch started, so no test leaves it running.
func awaitTracedAttemptDone(t *testing.T, events <-chan AttemptEvent) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Kind == AttemptDone {
				return
			}
		case <-deadline:
			t.Fatal("the launched attempt never reported that it finished")
		}
	}
}

// TraceNonceCommitted launches one attempt on a host that answers nothing.
func TraceNonceCommitted(t *testing.T, events raceJournal) {
	t.Helper()
	coordinator := tracedCoordinator(events)
	coordinator.launch(scheduler.Assignment{
		Escrow: "escrow-1",
		Host:   "host-2",
		Nonce:  fakePrepared{nonce: 12, hostIdx: 2},
	}, RolePrimary, StartPrimary)
	awaitTracedAttemptDone(t, coordinator.events)
}

// TraceEscalationUnfilled reports an escalation the scheduler could not fill.
func TraceEscalationUnfilled(t *testing.T, events raceJournal) {
	t.Helper()
	coordinator := tracedCoordinator(events)
	coordinator.pickReason = "receipt_timeout"
	coordinator.reportUnfilledPick(scheduler.ErrNoAvailableHost)
}

// TraceAttemptCrowned crowns the first attempt to claim the client.
func TraceAttemptCrowned(t *testing.T, events raceJournal) {
	t.Helper()
	attempt := &liveAttempt{nonce: 13, participant: "host-3", cancel: func() {}}
	coordinator := tracedCoordinator(events, attempt)
	coordinator.crownWinner(attempt, crownFirstClaim)
}

// TraceAttemptFinished completes a losing attempt that carries every delivery field a finish line reports.
func TraceAttemptFinished(t *testing.T, events raceJournal) {
	t.Helper()
	outcome := widestFinishedOutcome()
	outcome.Terminal = TerminalLost
	attempt := &liveAttempt{nonce: 77, participant: "host-3", cancel: func() {}}
	coordinator := tracedCoordinator(events, attempt)
	coordinator.complete(attempt, AttemptEvent{Kind: AttemptDone, Nonce: 77, At: testEpoch, Outcome: &outcome})
}

// TraceAttemptFinishedWithNoOutcome completes an attempt whose goroutine reported nothing.
func TraceAttemptFinishedWithNoOutcome(t *testing.T, events raceJournal) {
	t.Helper()
	attempt := &liveAttempt{nonce: 78, participant: "host-4", cancel: func() {}}
	coordinator := tracedCoordinator(events, attempt)
	coordinator.complete(attempt, AttemptEvent{Kind: AttemptDone, Nonce: 78, At: testEpoch})
}

// TraceHostDiverged diverges one host twice: the first spends its replay, the second blocks it.
func TraceHostDiverged(t *testing.T, events raceJournal) {
	t.Helper()
	attempt := &liveAttempt{nonce: 79, hostIdx: 1, participant: "host-5", cancel: func() {}}
	coordinator := tracedCoordinator(events, attempt)
	coordinator.stateDiverged(attempt)
	coordinator.stateDiverged(attempt)
}

// TraceNonceStranded strands a committed nonce the race can no longer spend.
func TraceNonceStranded(t *testing.T, events raceJournal) {
	t.Helper()
	coordinator := tracedCoordinator(events)
	coordinator.strand(scheduler.Assignment{
		Escrow: "escrow-1",
		Host:   "host-6",
		Nonce:  fakePrepared{nonce: 91, hostIdx: 2},
	}, RoleSpeculative)
}

func TraceWinnerFinishLanded(t *testing.T, events raceJournal) {
	t.Helper()
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.engine.deps.Journal = events
	winner := widestFinishedOutcome()
	winner.Nonce, winner.Participant, winner.Completed = lateFinishNonce, "host-7", sim.clock.Now()
	sim.engine.heldWinners.hold(heldWinnerKey{escrowID: "escrow-1", nonce: lateFinishNonce}, heldWinner{
		outcome: RaceOutcome{RequestID: "request-1", EscrowID: "escrow-1"},
		attempt: winner,
	})
	sim.clock.advance(1500 * time.Millisecond)
	sim.engine.OnFinishApplied("escrow-1", lateFinishNonce)
}

func RunServedWinnerFinishedBeforeItsVote(t *testing.T, recorder raceMetrics, atRaceEnd func(samples []perf.Sample)) []perf.Sample {
	t.Helper()
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.recordInto(recorder)
	releaseVotes := sim.parkVotes(t)
	runServedWinnerWithOpenNonce(t, sim)
	awaitVoteEntered(t, sim)
	atRaceEnd(sim.perf.recorded())
	sim.target.finish(lateFinishNonce)
	releaseVotes()
	sim.settleAll()
	return sim.perf.recorded()
}

func RunServedWinnerUnfinishedAtItsVote(t *testing.T, recorder raceMetrics, atRaceEnd func(samples []perf.Sample)) []perf.Sample {
	t.Helper()
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.recordInto(recorder)
	harness := newQueueHarness()
	sim.engine.settles = harness.queue
	fireVote := fireVoteOnCleanup(t, harness)
	runServedWinnerWithOpenNonce(t, sim)
	awaitScheduled(t, harness, 1)
	atRaceEnd(sim.perf.recorded())
	fireVote()
	sim.settleAll()
	return sim.perf.recorded()
}
