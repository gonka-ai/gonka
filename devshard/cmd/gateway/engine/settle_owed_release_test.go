package engine

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. For a vote posted at once, a refused vote retried after its delay and a vote deferred to its execution deadline, settle one unsettled attempt on an engine whose registration holds an escrow release that records `OwedTimeoutVotes`.
//  2. Fire the queue's timers until the race's last vote is through, and wait for the registration to be released.
//  3. Assert the escrow release saw no vote owed, so `Stop` cannot return while the owed-vote gauge still counts a vote it waited for.
func TestARaceIsReleasedOnlyAfterItsLastVoteIsNoLongerOwed(t *testing.T) {
	cases := []struct {
		name  string
		drive func(t *testing.T) (*Engine, *queueHarness, func())
	}{
		{"posted at once", func(t *testing.T) (*Engine, *queueHarness, func()) {
			races, harness := retryEngine(t, &failingPoster{})
			return races, harness, func() { harness.fire(0) }
		}},
		{"retried after a refusal", func(t *testing.T) (*Engine, *queueHarness, func()) {
			poster := &failingPoster{failures: alwaysFailing(1)}
			races, harness := retryEngine(t, poster)
			return races, harness, func() {
				harness.fire(0)
				awaitScheduled(t, harness, 2)
				harness.advance(refusedVoteFirstRetryDelay)
				harness.fire(1)
			}
		}},
		{"deferred to the execution deadline", func(t *testing.T) (*Engine, *queueHarness, func()) {
			races, harness := limitedEngine(t, 1)
			executionDue := harness.now().Add(executionWindow)
			poster := newRecoveringPoster(harness, func(uint64) time.Time { return executionDue })
			races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
			return races, harness, func() {
				harness.fire(0)
				awaitScheduled(t, harness, 2)
				harness.advance(executionWindow)
				harness.fire(1)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			races, _, run := tc.drive(t)
			var owedAtRelease atomic.Int64
			owedAtRelease.Store(-1)
			registration := races.admit()
			registration.holdEscrow(func() { owedAtRelease.Store(races.OwedTimeoutVotes()) })

			races.settle(race(unsettledAttempt()), nil, registration)
			run()
			awaitReleased(t, registration)

			require.Zero(t, owedAtRelease.Load(), "the race's place in the Stop barrier was released while a vote was still counted as owed")
		})
	}
}
