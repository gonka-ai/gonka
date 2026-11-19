package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	frozenFloorDeadlineAhead = time.Hour
	frozenFloorBufferedPosts = 1
	stoppedGuardWindow       = 100 * time.Millisecond
)

type frozenFloorPoster struct {
	now    func() time.Time
	unripe atomic.Bool
	waits  chan struct{}
	posts  atomic.Int64
}

func newFrozenFloorPoster(now func() time.Time) *frozenFloorPoster {
	poster := &frozenFloorPoster{now: now, waits: make(chan struct{}, frozenFloorBufferedPosts)}
	poster.unripe.Store(true)
	return poster
}

func (p *frozenFloorPoster) VoteDeadline(uint64, time.Time) time.Time {
	if p.unripe.Load() {
		return p.now().Add(frozenFloorDeadlineAhead)
	}
	return postAtOnce
}

func (p *frozenFloorPoster) SettleTimeout(ctx context.Context, _ TimeoutStep) (TimeoutVote, error) {
	p.posts.Add(1)
	p.waits <- struct{}{}
	<-ctx.Done()
	return TimeoutVote{}, ctx.Err()
}

func stopWithin(t *testing.T, races *Engine, what string) {
	t.Helper()
	stopped := make(chan struct{})
	go func() { races.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatalf("Stop did not return within 2s: %s", what)
	}
}

// Test flow:
//  1. Settle a refused attempt through a poster whose vote deadline is always an hour ahead, and fire the queue's timer so the deadline slides once.
//  2. Stop the engine and assert it returns within two seconds.
//  3. Assert no vote was posted, the book was never paid the nonce, and the race's registration is released.
func TestStopReturnsWhenAQueuedVotesDeadlineKeepsSliding(t *testing.T) {
	races, harness, book := bookedEngine(t, nil, nil)
	poster := newFrozenFloorPoster(harness.now)
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	registration := races.admit()
	attempt := unsettledAttempt()

	races.settle(race(attempt), nil, registration)
	harness.fire(0)
	awaitScheduled(t, harness, 2)
	stopWithin(t, races, "a vote whose deadline slides was waited on")

	require.Zero(t, poster.posts.Load(), "a vote that is not due is not posted at Stop")
	owed, paid := book.snapshot()
	require.Equal(t, []uint64{attempt.Nonce}, owed)
	require.Empty(t, paid, "the vote stays owed in the book for the next gateway")
	require.True(t, registration.released.Load(), "the race hands its vote to the store and releases its registration")
	require.Zero(t, races.OwedTimeoutVotes(), "the queue holds nothing after Stop")
}

// Test flow:
//  1. For a race whose escrow is present and for one marked escrow-missing, settle a refused attempt through a poster that is due, fire the timer and wait until its post is waiting.
//  2. Make the vote unripe again, as a frozen floor does, and Stop the engine.
//  3. Assert Stop returns within two seconds, the book was never paid the nonce, and the registration is released.
func TestStopCancelsAPostWaitingOnAFrozenFloor(t *testing.T) {
	for _, escrowMissing := range []bool{false, true} {
		t.Run(fmt.Sprintf("escrow missing %t", escrowMissing), func(t *testing.T) {
			races, harness, book := bookedEngine(t, nil, nil)
			poster := newFrozenFloorPoster(harness.now)
			poster.unripe.Store(false)
			races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
			registration := races.admit()
			outcome := race(unsettledAttempt())
			outcome.Lifecycle.EscrowMissing = escrowMissing

			races.settle(outcome, nil, registration)
			harness.fire(0)
			select {
			case <-poster.waits:
			case <-time.After(2 * time.Second):
				t.Fatal("the due vote's post never started waiting")
			}
			poster.unripe.Store(true)
			stopWithin(t, races, "a post waiting on a frozen floor was waited on")

			_, paid := book.snapshot()
			require.Empty(t, paid, "a post cut by Stop leaves its vote owed")
			require.True(t, registration.released.Load())
		})
	}
}

// Test flow:
//  1. Settle a refused attempt through a poster that is due, fire the timer and wait until its post is in flight.
//  2. Stop the engine while the vote stays due, wait until the engine is stopping, and assert Stop stays blocked for 100 ms.
//  3. Let the post return and assert Stop returns within two seconds and the post's context was never cancelled.
func TestStopDoesNotCancelAVoteThatIsDue(t *testing.T) {
	races, harness, _ := bookedEngine(t, nil, nil)
	poster := &releasablePoster{started: make(chan struct{}), release: make(chan struct{})}
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }

	races.settle(race(unsettledAttempt()), nil, races.admit())
	harness.fire(0)
	select {
	case <-poster.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the due vote's post never started")
	}
	stopped := make(chan struct{})
	go func() { races.Stop(); close(stopped) }()
	require.Eventually(t, func() bool { return races.stopping.Err() != nil }, 2*time.Second, time.Millisecond,
		"Stop never began")
	require.Never(t, func() bool {
		select {
		case <-stopped:
			return true
		default:
			return false
		}
	}, stoppedGuardWindow, time.Millisecond, "Stop returned while a due vote was still being posted")
	close(poster.release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the due vote's post returned")
	}

	require.False(t, poster.cancelled.Load(), "a vote that is due is posted on a live context")
}

type releasablePoster struct {
	started   chan struct{}
	release   chan struct{}
	cancelled atomic.Bool
}

func (p *releasablePoster) VoteDeadline(uint64, time.Time) time.Time { return postAtOnce }

func (p *releasablePoster) SettleTimeout(ctx context.Context, _ TimeoutStep) (TimeoutVote, error) {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		p.cancelled.Store(true)
	}
	return TimeoutVote{Kind: TimeoutKindRefused}, nil
}
