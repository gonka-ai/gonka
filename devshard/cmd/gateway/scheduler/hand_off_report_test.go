package scheduler

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// Test flow:
//  1. Build a harness and submit a request, then await its reply.
//  2. Assert the submit succeeded.
//  3. Assert exactly one assignment was reported.
//  4. Assert that assignment's nonce and request id match the ones handed off.
func TestAHandedOffNonceIsReportedWithItsRequest(t *testing.T) {
	test := newHarness(t, harnessConfig{})

	queued := test.submit(t, test.clock.Now())
	result := awaitReply(t, queued)

	if result.err != nil {
		t.Fatalf("submit: %v", result.err)
	}
	assigned := test.observer.assignments()
	if len(assigned) != 1 {
		t.Fatalf("reported %+v, want the one nonce handed off", assigned)
	}
	if assigned[0].nonce != result.assignment.Nonce.Nonce() {
		t.Fatalf("reported nonce %d, want the %d that was handed off", assigned[0].nonce, result.assignment.Nonce.Nonce())
	}
	if assigned[0].requestID != queued.profile.RequestID {
		t.Fatalf("reported request %q, want %q", assigned[0].requestID, queued.profile.RequestID)
	}
}

// Test flow:
//  1. Submit a request that entered the queue a moment before the dispatcher's clock.
//  2. Await its reply.
//  3. Assert the hand-off reported exactly that wait: the time a request spends in an escrow's queue is what the queue-wait histogram reads.
func TestAHandedOffNonceReportsHowLongItsRequestQueued(t *testing.T) {
	test := newHarness(t, harnessConfig{})
	const queuedFor = 250 * time.Millisecond

	queued := test.submit(t, test.clock.Now().Add(-queuedFor))
	awaitReply(t, queued)

	if waits := test.observer.waits(); !slices.Equal(waits, []time.Duration{queuedFor}) {
		t.Fatalf("reported waits %v, want [%v]", waits, queuedFor)
	}
}

// Test flow:
//  1. Submit a waiter that abandons itself right after the decision is made.
//  2. Submit and await a second request that takes its place.
//  3. Assert no reported assignment carries the abandoned waiter's request id.
func TestAnAbandonedNonceIsNotReportedAsHandedOff(t *testing.T) {
	var abandonOnce sync.Once
	var lost *waiter
	test := newHarness(t, harnessConfig{afterDecide: func(HostBinding) {
		abandonOnce.Do(func() { lost.abandoned.Store(true) })
	}})

	lost = newWaiter(RequestProfile{Model: modelA, RequestID: "req-lost"}, test.clock.Now())
	if outcome := test.dispatcher.submitWaiter(lost); outcome != submitAccepted {
		t.Fatalf("submitWaiter = %v, want submitAccepted", outcome)
	}
	next := test.submit(t, test.clock.Now())
	awaitReply(t, next)

	for _, assigned := range test.observer.assignments() {
		if assigned.requestID == "req-lost" {
			t.Fatalf("reported %+v for a nonce nobody received", assigned)
		}
	}
}
