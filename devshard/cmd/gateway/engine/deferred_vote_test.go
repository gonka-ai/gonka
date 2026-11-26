package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/config"
)

const executionWindow = 32 * time.Minute

type recoveringPoster struct {
	mu        sync.Mutex
	harness   *queueHarness
	recovered map[uint64]bool
	started   map[uint64]time.Time
	dueAt     func(nonce uint64) time.Time
	hold      func(nonce uint64)
}

func newRecoveringPoster(harness *queueHarness, dueAt func(nonce uint64) time.Time) *recoveringPoster {
	return &recoveringPoster{harness: harness, recovered: map[uint64]bool{}, started: map[uint64]time.Time{}, dueAt: dueAt}
}

func (p *recoveringPoster) VoteDeadline(nonce uint64, _ time.Time) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.recovered[nonce] {
		return time.Time{}
	}
	return p.dueAt(nonce)
}

func (p *recoveringPoster) Voteable(uint64) bool { return true }

func (p *recoveringPoster) SettleTimeout(_ context.Context, step TimeoutStep) (TimeoutVote, error) {
	p.mu.Lock()
	if !p.recovered[step.Nonce] {
		p.recovered[step.Nonce] = true
		p.mu.Unlock()
		return TimeoutVote{Deferred: true}, nil
	}
	p.started[step.Nonce] = p.harness.now()
	hold := p.hold
	p.mu.Unlock()
	if hold != nil {
		hold(step.Nonce)
	}
	return TimeoutVote{Kind: TimeoutKindExecution}, nil
}

func (p *recoveringPoster) startedAt(nonce uint64) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, started := p.started[nonce]
	return at, started
}

func limitedEngine(t *testing.T, limit int64) (*Engine, *queueHarness) {
	t.Helper()
	settings := engineSettings(EscalationPolicy{}, config.Modes{})
	settings.Engine.MaxConcurrentTimeoutVotes = limit
	deps := completeEngineDeps(t)
	deps.Config = config.NewHolder(settings)
	races, err := NewEngine(deps)
	require.NoError(t, err)
	harness := newQueueHarness()
	races.settles = harness.queue
	return races, harness
}

func (q *settleQueue) fullAt(limit int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.posting >= limit
}

// Test flow:
//  1. Build an engine with one voting place and a poster whose first vote comes back deferred because a recovery receipt moved the deadline 32 minutes out; settle one unsettled attempt and fire its timer.
//  2. Assert the vote was handed back as a new timer 32 minutes out, the race stays registered and owes one vote.
//  3. Add a second vote due now and fire it; assert it posts at once, so the deferred vote holds no place while it waits.
//  4. Advance the clock to the execution deadline and fire the deferred timer.
//  5. Assert the vote posted exactly at the execution deadline, the registration was released and nothing is owed.
func TestAVoteRefusedThenRecoveredFreesItsPlaceAndPostsAtTheExecutionDeadline(t *testing.T) {
	races, harness := limitedEngine(t, 1)
	executionDue := harness.now().Add(executionWindow)
	poster := newRecoveringPoster(harness, func(uint64) time.Time { return executionDue })
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	registration := races.admit()

	races.settle(race(unsettledAttempt()), nil, registration)
	harness.fire(0)
	awaitScheduled(t, harness, 2)

	require.Equal(t, executionWindow, harness.scheduledDelays()[1], "the deferred vote waits on a timer for the execution deadline")
	require.False(t, registration.released.Load(), "the race stays registered while its vote is deferred")
	require.Equal(t, int64(1), races.OwedTimeoutVotes())
	harness.add("neighbour", 0, 1)
	harness.fire(2)
	require.Equal(t, "neighbour", harness.nextPosted(t), "the only place was free while the vote waited")
	harness.advance(executionWindow)
	harness.fire(1)
	awaitReleased(t, registration)
	postedAt, started := poster.startedAt(7)
	require.True(t, started, "the deferred vote was posted")
	require.Equal(t, executionDue, postedAt)
	require.Eventually(t, func() bool { return races.OwedTimeoutVotes() == 0 }, 2*time.Second, time.Millisecond, "the deferred vote is no longer owed once posted")
}

// Test flow:
//  1. Build an engine with 64 voting places and 200 races whose first vote is deferred, each to its own execution deadline one second after the one before; fire the 200 first timers one at a time.
//  2. Step virtual time one second at a time, firing each deferred timer when its deadline arrives, and releasing a post a minute after it started.
//  3. Assert every deferred vote started no later than its deadline plus one post time, since a waiting vote holds no place.
func TestTwoHundredDeferredVotesStartOnTimeThroughSixtyFourPlaces(t *testing.T) {
	const votes, places, firstNonce = 200, 64, 100
	const postTime, spacing = time.Minute, time.Second
	releases := make(map[uint64]chan struct{}, votes)
	for index := range votes {
		releases[uint64(firstNonce+index)] = make(chan struct{})
	}
	races, harness := limitedEngine(t, places)
	base := harness.now()
	poster := newRecoveringPoster(harness, func(nonce uint64) time.Time {
		return base.Add(time.Duration(nonce-firstNonce) * spacing)
	})
	poster.hold = func(nonce uint64) { <-releases[nonce] }
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	for index := range votes {
		attempt := unsettledAttempt()
		attempt.Nonce = uint64(firstNonce + index)
		races.settle(race(attempt), nil, races.admit())
	}
	for index := range votes {
		harness.fire(index)
		awaitScheduled(t, harness, votes+index+1)
	}

	released := map[uint64]bool{}
	for second := range votes + int(postTime/spacing) {
		if second > 0 {
			harness.advance(spacing)
		}
		if second < votes {
			harness.fire(votes + second)
			nonce := uint64(firstNonce + second)
			require.Eventually(t, func() bool {
				_, started := poster.startedAt(nonce)
				return started || harness.queue.fullAt(places)
			}, 2*time.Second, time.Millisecond, "vote %d neither started nor found the places full", nonce)
		}
		for nonce, release := range releases {
			if at, started := poster.startedAt(nonce); started && !released[nonce] && !at.Add(postTime).After(harness.now()) {
				released[nonce] = true
				close(release)
			}
		}
	}
	for nonce, release := range releases {
		if !released[nonce] {
			close(release)
		}
	}

	for index := range votes {
		nonce := uint64(firstNonce + index)
		require.Eventually(t, func() bool { _, started := poster.startedAt(nonce); return started }, 2*time.Second, time.Millisecond, "vote %d never started", nonce)
		startedAt, _ := poster.startedAt(nonce)
		require.LessOrEqual(t, startedAt.Sub(poster.dueAt(nonce)), postTime, "vote %d started later than its deadline plus one post time", nonce)
	}
	races.Stop()
}

// Test flow:
//  1. Build an engine and a race of two unsettled attempts whose votes are both deferred, each to a different execution deadline, and fire the race's first timer.
//  2. Assert two deferred tasks were armed, one per deadline, and the race stays registered.
//  3. Fire the earlier deferred task; assert the race is still registered, since the later deferred vote is not posted yet.
//  4. Fire the later one; assert both votes posted and the registration was released exactly then.
func TestARaceWithTwoDeferredVotesIsSettledOnlyAfterTheLastOne(t *testing.T) {
	races, harness := limitedEngine(t, 4)
	first, second := harness.now().Add(executionWindow), harness.now().Add(2*executionWindow)
	poster := newRecoveringPoster(harness, func(nonce uint64) time.Time {
		if nonce == 11 {
			return first
		}
		return second
	})
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	earlier, later := unsettledAttempt(), unsettledAttempt()
	earlier.Nonce, later.Nonce = 11, 12
	outcome := race(earlier)
	outcome.Attempts = []AttemptOutcome{earlier, later}
	registration := races.admit()

	races.settle(outcome, nil, registration)
	harness.fire(0)
	awaitScheduled(t, harness, 3)

	require.False(t, registration.released.Load())
	harness.advance(executionWindow)
	harness.fire(1)
	require.Eventually(t, func() bool { _, started := poster.startedAt(11); return started }, 2*time.Second, time.Millisecond, "the earlier deferred vote never posted")
	require.False(t, registration.released.Load(), "the later deferred vote is still owed")
	harness.advance(executionWindow)
	harness.fire(2)
	awaitReleased(t, registration)
	_, started := poster.startedAt(12)
	require.True(t, started, "the later deferred vote posted")
}
