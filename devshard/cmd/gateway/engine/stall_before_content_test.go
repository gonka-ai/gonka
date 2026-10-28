package engine

import (
	"context"
	"testing"
	"time"
)

const (
	stallBeforeContentWindow = 500 * time.Millisecond
	emptyDeltaInterval       = 40 * time.Millisecond
	deltasPastTheStall       = 20
	silentHostNonce          = 10
	rescueHostNonce          = 11
)

const (
	emptyDeltaEvent     = `data: {"choices":[{"delta":{}}]}` + "\n\n"
	reasoningDeltaEvent = `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}` + "\n\n"
)

func stallBeforeContentPolicy() EscalationPolicy {
	policy := settledPolicy()
	policy.InterChunkStall = stallBeforeContentWindow
	policy.MaxAttemptsPerRequest = 2
	return policy
}

// rescueHost queues a second host that answers at once, and reports when the race dispatches to it.
func rescueHost(sim *simulator) <-chan uint64 {
	arrive := make(chan uint64, 1)
	sim.host(rescueHostNonce, 1, "rescue-host", &hostScript{
		arrive: arrive, receipt: true, chunks: []string{contentEvent("hi")}, confirmed: true, finished: true,
	})
	return arrive
}

func runInBackground(t *testing.T, sim *simulator) <-chan RaceOutcome {
	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := sim.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	return outcomes
}

// feedDeltas sends one delta per interval of virtual time, for longer than the stall window.
func feedDeltas(sim *simulator, feed chan<- string, delta string) {
	for range deltasPastTheStall {
		feed <- delta
		sim.clock.advance(emptyDeltaInterval)
	}
}

func assertRescued(t *testing.T, sim *simulator, rescued <-chan uint64, outcomes <-chan RaceOutcome) {
	t.Helper()
	waitForValue(t, rescued, "a dispatch to the rescue host")
	waitForValue(t, outcomes, "the race's answer")
	if reported := sim.reported(t); reported.WinnerNonce != rescueHostNonce {
		t.Fatalf("winner = %d, want the rescue host %d", reported.WinnerNonce, rescueHostNonce)
	}
}

// Test flow:
//  1. Start a two-host race whose primary sends a role chunk and then nothing, with a stall window far shorter than every other deadline.
//  2. Wait for the stall to arm and move the virtual clock past it.
//  3. Assert the race dispatches to the second host and that host serves the answer: a role chunk proves nothing, so the stall runs from it.
func TestARoleChunkFollowedBySilenceFallsBackOnTheStall(t *testing.T) {
	sim := newSimulator(t, stallBeforeContentPolicy(), 2, qwenModel)
	streaming, resume := make(chan uint64, 1), make(chan struct{})
	sim.host(silentHostNonce, 0, "silent-host", &hostScript{receipt: true, chunks: []string{roleEvent}, streaming: streaming, resume: resume})
	rescued := rescueHost(sim)

	outcomes := runInBackground(t, sim)
	<-streaming
	sim.clock.waitArmed(t, stallBeforeContentWindow)
	sim.clock.advance(stallBeforeContentWindow + time.Millisecond)

	waitForValue(t, rescued, "a dispatch to the rescue host")
	close(resume)
	assertRescued(t, sim, closedDispatch(), outcomes)
	sim.settleAll()
}

// Test flow:
//  1. Start a two-host race whose primary sends a role chunk and then an empty delta every 40 ms of virtual time, for longer than the stall window.
//  2. Assert the race dispatches to the second host and that host serves the answer: an empty delta carries nothing, so it never moves the stall clock.
func TestEmptyDeltasDoNotKeepAnAttemptAlive(t *testing.T) {
	sim := newSimulator(t, stallBeforeContentPolicy(), 2, qwenModel)
	feed := make(chan string)
	sim.host(silentHostNonce, 0, "empty-delta-host", &hostScript{receipt: true, chunks: []string{roleEvent}, feed: feed})
	rescued := rescueHost(sim)

	outcomes := runInBackground(t, sim)
	feedDeltas(sim, feed, emptyDeltaEvent)

	waitForValue(t, rescued, "a dispatch to the rescue host")
	close(feed)
	assertRescued(t, sim, closedDispatch(), outcomes)
	sim.settleAll()
}

// Test flow:
//  1. Start a two-host race whose primary sends a role chunk and then only reasoning_content, one delta every 40 ms of virtual time for longer than the stall window, and then finishes.
//  2. Assert the primary wins alone and the second host is never dispatched: reasoning is content, so a model thinking before it answers is not cut.
func TestAReasoningOnlyStreamIsNotCutByTheStall(t *testing.T) {
	sim := newSimulator(t, stallBeforeContentPolicy(), 2, qwenModel)
	feed := make(chan string)
	sim.host(silentHostNonce, 0, "reasoning-host", &hostScript{receipt: true, chunks: []string{roleEvent}, feed: feed, confirmed: true, finished: true})
	rescued := rescueHost(sim)

	outcomes := runInBackground(t, sim)
	feedDeltas(sim, feed, reasoningDeltaEvent)
	close(feed)
	waitForValue(t, outcomes, "the race's answer")

	reported := sim.reported(t)
	if reported.WinnerNonce != silentHostNonce || len(reported.Attempts) != 1 {
		t.Fatalf("winner = %d across %d attempts, want the reasoning host alone", reported.WinnerNonce, len(reported.Attempts))
	}
	if len(rescued) != 0 {
		t.Fatal("the race dispatched a second host beside a model that was reasoning")
	}
	sim.settleAll()
}

func closedDispatch() <-chan uint64 {
	dispatched := make(chan uint64, 1)
	dispatched <- rescueHostNonce
	return dispatched
}
