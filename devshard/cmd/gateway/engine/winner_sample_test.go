package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

const lateFinishNonce uint64 = 10

func runServedWinnerWithOpenNonce(t *testing.T, sim *simulator) {
	t.Helper()
	runServedWinner(t, sim, false)
}

func runServedWinner(t *testing.T, sim *simulator, finishLater bool) {
	t.Helper()
	sim.clock.step = time.Millisecond
	body := string(readFixture(t, "content_stream.sse"))
	sim.host(lateFinishNonce, 0, "host-0", &hostScript{
		receipt:     true,
		chunks:      []string{body[:40], body[40:]},
		confirmed:   true,
		finishLater: finishLater,
	})
	outcome, err := sim.run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !outcome.Succeeded || outcome.WinnerNonce != lateFinishNonce {
		t.Fatalf("outcome = winner %d succeeded %v, want winner %d succeeded true", outcome.WinnerNonce, outcome.Succeeded, lateFinishNonce)
	}
	if got := string(sim.client.forwarded()); got != body {
		t.Fatalf("client stream differs from the host stream:\n got %q\nwant %q", got, body)
	}
	if winner := attemptFor(t, sim.reported(t), lateFinishNonce); winner.Terminal != TerminalWon || winner.NonceFinished {
		t.Fatalf("winner = %v finished %v, want TerminalWon with its nonce still open", winner.Terminal, winner.NonceFinished)
	}
}

func awaitVoteEntered(t *testing.T, sim *simulator) {
	t.Helper()
	select {
	case nonce := <-sim.poster.entered:
		if nonce != lateFinishNonce {
			t.Fatalf("vote entered for nonce %d, want %d", nonce, lateFinishNonce)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the winner's execution vote never reached the poster")
	}
}

func fireVoteOnCleanup(t *testing.T, harness *queueHarness) func() {
	t.Helper()
	fire := sync.OnceFunc(func() {
		if len(harness.scheduledDelays()) > 0 {
			harness.fire(0)
		}
	})
	t.Cleanup(fire)
	return fire
}

type teeMetrics struct {
	raceMetrics
	sim *simMetrics
}

func (m teeMetrics) RecordRace(outcome RaceOutcome) {
	m.raceMetrics.RecordRace(outcome)
	m.sim.RecordRace(outcome)
}

func (s *simulator) recordInto(recorder raceMetrics) {
	s.engine.deps.Metrics = teeMetrics{raceMetrics: recorder, sim: s.metrics}
}

func assertOneSample(t *testing.T, sim *simulator, responsive bool) {
	t.Helper()
	samples := sim.perf.recorded()
	if len(samples) != 1 || samples[0].ParticipantKey != "host-0" || samples[0].Responsive != responsive {
		t.Fatalf("samples = %+v, want one host-0 sample with Responsive %v", samples, responsive)
	}
}

// Test flow:
//  1. Start a one-host simulator, park its votes, and script host-0 to stream a whole answer with a receipt and a confirmed start while its nonce stays open.
//  2. Run the race and wait until the winner's execution vote reaches the poster.
//  3. Assert no perf sample is recorded while the nonce is open.
//  4. Apply the winner's finish and report it to the engine as applied.
//  5. Assert one responsive host-0 sample is recorded while the vote is still parked.
//  6. Let the vote return, stop the engine, and assert the sample is still the only one.
func TestServedWinnerSampleIsDecidedWhenItsFinishApplies(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	releaseVotes := sim.parkVotes(t)
	runServedWinnerWithOpenNonce(t, sim)
	awaitVoteEntered(t, sim)

	if samples := sim.perf.recorded(); len(samples) != 0 {
		t.Fatalf("samples = %+v, want none before the winner's nonce finishes", samples)
	}
	sim.target.finish(lateFinishNonce)
	sim.engine.OnFinishApplied("escrow-1", lateFinishNonce)
	assertOneSample(t, sim, true)

	releaseVotes()
	sim.settleAll()
	assertOneSample(t, sim, true)
}

// Test flow:
//  1. Start a one-host simulator whose vote timers fire only on command, and script host-0 to stream a whole answer with a receipt and a confirmed start while its nonce reads open once and finished from then on.
//  2. Run the race and assert the client got the whole answer at once from a TerminalWon winner reported with its nonce still open.
//  3. Wait until the winner's execution vote is scheduled, without firing its deadline.
//  4. Assert exactly one perf sample for host-0, and that it is responsive.
//  5. Fire the vote's deadline so the engine can stop.
func TestServedWinnerFinishedBeforeItsSampleIsHeldRecordsAResponsiveSample(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	harness := newQueueHarness()
	sim.engine.settles = harness.queue
	fireVote := fireVoteOnCleanup(t, harness)
	runServedWinner(t, sim, true)

	awaitScheduled(t, harness, 1)

	assertOneSample(t, sim, true)
	fireVote()
}

func winnerFinishSteps(journal *recordingJournal) []RaceStep {
	var landed []RaceStep
	for _, step := range journal.recorded() {
		if step.Kind == RaceStepWinnerFinishLanded {
			landed = append(landed, step)
		}
	}
	return landed
}

// Test flow:
//  1. Start a one-host simulator recording its journal, park its votes, and script host-0 to stream a whole answer with a receipt and a confirmed start while its nonce stays open.
//  2. Run the race, wait until the winner's execution vote reaches the poster, apply the winner's finish, let the vote return and stop the engine.
//  3. Assert the journal recorded one served-winner finish step for host-0's nonce on escrow-1.
func TestServedWinnerFinishedBeforeItsVoteJournalsItsFinish(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	journal := &recordingJournal{}
	sim.engine.deps.Journal = journal
	releaseVotes := sim.parkVotes(t)
	runServedWinnerWithOpenNonce(t, sim)

	awaitVoteEntered(t, sim)
	sim.target.finish(lateFinishNonce)
	releaseVotes()
	sim.settleAll()

	landed := winnerFinishSteps(journal)
	if len(landed) != 1 || landed[0].Nonce != lateFinishNonce || landed[0].EscrowID != "escrow-1" || landed[0].Participant != "host-0" {
		t.Fatalf("finish steps = %+v, want one for host-0's nonce %d on escrow-1", landed, lateFinishNonce)
	}
}

// Test flow:
//  1. Start a one-host simulator recording its journal, let its vote timers fire only on command, and script host-0 to stream a whole answer with a receipt and a confirmed start while its nonce never finishes.
//  2. Run the race, wait until the winner's execution vote is scheduled, fire its deadline and stop the engine.
//  3. Assert the winner was decided with one unresponsive sample and the journal recorded no served-winner finish step.
func TestServedWinnerUnfinishedAtItsVoteJournalsNoFinish(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	journal := &recordingJournal{}
	sim.engine.deps.Journal = journal
	harness := newQueueHarness()
	sim.engine.settles = harness.queue
	fireVote := fireVoteOnCleanup(t, harness)
	runServedWinnerWithOpenNonce(t, sim)

	awaitScheduled(t, harness, 1)
	fireVote()
	sim.settleAll()

	assertOneSample(t, sim, false)
	if landed := winnerFinishSteps(journal); len(landed) != 0 {
		t.Fatalf("finish steps = %+v, want none for a nonce still open at its vote", landed)
	}
}
