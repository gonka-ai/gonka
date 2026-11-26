package engine

import (
	"context"
	"testing"
	"time"
)

// Test flow:
//  1. Plan the deadlines of an attempt still waiting on its catch-up, of one whose request has left, and of two whose catch-up found the host unreachable 10 s and 70 s after launch.
//  2. Assert the waiting attempt's clock starts at the latest a minute after its launch, the sent one's at its send, and the faulted ones' at the earlier of the fault and that minute.
func TestACatchUpHoldsAnAttemptsClockForAMinuteAtMost(t *testing.T) {
	waiting := &liveAttempt{nonce: 600, participant: "host-0", launchTime: testEpoch, cancel: func() {}}
	sent := &liveAttempt{nonce: 601, participant: "host-1", launchTime: testEpoch, sendTime: testEpoch.Add(5 * time.Second), cancel: func() {}}
	faulted := &liveAttempt{nonce: 602, participant: "host-2", launchTime: testEpoch, catchUpFault: testEpoch.Add(10 * time.Second), cancel: func() {}}
	lateFault := &liveAttempt{nonce: 603, participant: "host-3", launchTime: testEpoch, catchUpFault: testEpoch.Add(70 * time.Second), cancel: func() {}}
	coordinator := pausedCoordinator(newRaceFixture(settledPolicy(), 4), 4, waiting, sent, faulted, lateFault)

	plan := coordinator.plan()

	if got, want := plan.Attempts[0].ClockStart, testEpoch.Add(maxCatchUpBeforeSend); !got.Equal(want) {
		t.Fatalf("waiting attempt's clock starts at %v, want %v", got, want)
	}
	if got, want := plan.Attempts[1].ClockStart, testEpoch.Add(5*time.Second); !got.Equal(want) {
		t.Fatalf("sent attempt's clock starts at %v, want %v", got, want)
	}
	if got, want := plan.Attempts[2].ClockStart, testEpoch.Add(10*time.Second); !got.Equal(want) {
		t.Fatalf("faulted attempt's clock starts at %v, want %v", got, want)
	}
	if got, want := plan.Attempts[3].ClockStart, testEpoch.Add(maxCatchUpBeforeSend); !got.Equal(want) {
		t.Fatalf("late-faulted attempt's clock starts at %v, want %v", got, want)
	}
}

// Test flow:
//  1. Start a race whose primary's catch-up finds its host unreachable 10 s after launch and gives up only 42 s after it, with a healthy second host queued.
//  2. Advance the clock to the fault, then by the receipt timeout.
//  3. Assert the escalation reaches the second host at fault + receipt timeout, while the catch-up is still failing, and serves the race.
func TestAnUnreachableCatchUpStartsTheAttemptsClockAtItsFirstFault(t *testing.T) {
	policy := EscalationPolicy{
		ReceiptTimeout:        5 * time.Second,
		FirstTokenFloor:       6 * time.Second,
		InterChunkStall:       time.Hour,
		LoserGrace:            10 * time.Minute,
		MaxAttemptsPerRequest: 2,
	}
	fixture := newRaceFixture(policy, 2)
	inCatchUp, fault, fail := make(chan uint64, 1), make(chan struct{}), make(chan struct{})
	fixture.host(90, 0, "host-0", &hostScript{catchUp: &catchUpScript{arrive: inCatchUp, fault: fault, fail: fail}})
	rival := make(chan uint64, 1)
	fixture.host(91, 1, "host-1", &hostScript{
		arrive:    rival,
		receipt:   true,
		chunks:    []string{contentChunk(91)},
		confirmed: true,
		finished:  true,
	})
	returned := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		returned <- outcome
	}()
	waitForValue(t, inCatchUp, "the primary entering its catch-up")

	fixture.clock.advance(10 * time.Second)
	close(fault)
	fixture.clock.waitArmed(t, policy.ReceiptTimeout)
	fixture.clock.advance(policy.ReceiptTimeout)

	waitForValue(t, rival, "the escalation reaching the second host")
	if sent, want := fixture.clock.Now(), testEpoch.Add(15*time.Second); !sent.Equal(want) {
		t.Fatalf("escalation sent at %v, want %v: the first fault plus the receipt timeout", sent.Sub(testEpoch), want.Sub(testEpoch))
	}
	waitForValue(t, returned, "the race being served")
	fixture.clock.advance(27 * time.Second)
	close(fail)
	reported := waitForValue(t, fixture.reported, "the race's report")
	if len(reported.Attempts) != 2 || reported.Attempts[1].StartReason != EscalationReasonReceipt || reported.Attempts[1].Terminal != TerminalWon {
		t.Fatalf("attempts = %+v, want the receipt-timeout escalation to win", reported.Attempts)
	}
}
