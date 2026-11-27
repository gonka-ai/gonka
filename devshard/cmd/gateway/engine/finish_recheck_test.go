package engine

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type queuedFinishTarget struct {
	DispatchTarget
	applied atomic.Bool
}

func (t *queuedFinishTarget) NonceFinished(uint64) bool { return true }

func (t *queuedFinishTarget) NonceCanonicalFinished(uint64) bool { return t.applied.Load() }

type farPoster struct {
	*failingPoster
	harness *queueHarness
}

func (p farPoster) VoteDeadline(uint64, time.Time) time.Time {
	return p.harness.now().Add(32 * time.Minute)
}

// Test flow:
//  1. Settle an unsettled attempt whose Finish is only queued, through a poster whose vote is due 32 minutes out.
//  2. Assert the queue's first timer is the recheck interval, not the deadline, and the registration stays held.
//  3. Fire it with the Finish still queued, and assert the queue re-arms the same interval with nothing posted.
//  4. Apply the Finish and fire the re-armed timer, and assert the vote posts at once and the registration is released.
func TestAVoteWhoseFinishAppliesEarlyIsPostedAtTheNextRecheck(t *testing.T) {
	poster := &failingPoster{}
	races, harness := retryEngine(t, nil)
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) {
		return farPoster{failingPoster: poster, harness: harness}, true
	}
	registration := races.admit()
	target := &queuedFinishTarget{}
	registration.holdTarget(target, nil)

	races.settle(race(unsettledAttempt()), nil, registration)

	awaitScheduled(t, harness, 1)
	require.Equal(t, finishRecheckInterval, harness.scheduledDelays()[0], "a queued Finish is rechecked sooner than the vote's deadline")
	require.False(t, registration.released.Load(), "the registration is held while the vote is owed")

	harness.advance(finishRecheckInterval)
	harness.fire(0)
	awaitScheduled(t, harness, 2)
	require.Equal(t, finishRecheckInterval, harness.scheduledDelays()[1], "a Finish still queued re-arms the recheck")
	require.Zero(t, poster.postCount(), "a vote not yet due is not posted")

	target.applied.Store(true)
	harness.advance(finishRecheckInterval)
	harness.fire(1)
	awaitPosts(t, poster, 1)
	awaitReleased(t, registration)
}

// Test flow:
//  1. Settle an unsettled attempt whose Finish is only queued, through a poster whose vote is due 32 minutes out.
//  2. Apply the Finish, then stop the engine before any recheck fires.
//  3. Assert the vote reaches the poster, which skips it for an applied Finish, rather than being left owed as a vote not yet due.
func TestAVoteWhoseFinishAppliedIsPostedAtStopNotLeftOwed(t *testing.T) {
	poster := &failingPoster{}
	races, harness := retryEngine(t, nil)
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) {
		return farPoster{failingPoster: poster, harness: harness}, true
	}
	registration := races.admit()
	target := &queuedFinishTarget{}
	registration.holdTarget(target, nil)

	races.settle(race(unsettledAttempt()), nil, registration)
	awaitScheduled(t, harness, 1)
	target.applied.Store(true)
	races.Stop()

	require.Equal(t, 1, poster.postCount(), "a vote whose Finish applied is posted at Stop, not left owed")
	awaitReleased(t, registration)
}
