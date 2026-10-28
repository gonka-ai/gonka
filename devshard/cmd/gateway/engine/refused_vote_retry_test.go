package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/user"
)

type failingPoster struct {
	mu       sync.Mutex
	failures []error
	posts    int
}

func (p *failingPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *failingPoster) SettleTimeout(context.Context, TimeoutStep) (TimeoutVote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts++
	if len(p.failures) == 0 {
		return TimeoutVote{Kind: TimeoutKindRefused}, nil
	}
	failure := p.failures[0]
	p.failures = p.failures[1:]
	return TimeoutVote{}, failure
}

func (p *failingPoster) postCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.posts
}

func alwaysFailing(count int) []error {
	failures := make([]error, count)
	for position := range failures {
		failures[position] = fmt.Errorf("send timeout diff: %w", errVoteCause)
	}
	return failures
}

func retryEngine(t *testing.T, poster TimeoutPoster) (*Engine, *queueHarness) {
	t.Helper()
	deps := completeEngineDeps(t)
	deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	races, err := NewEngine(deps)
	require.NoError(t, err)
	harness := newQueueHarness()
	races.settles = harness.queue
	return races, harness
}

func (h *queueHarness) advance(by time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(by)
}

func awaitPosts(t *testing.T, poster *failingPoster, want int) {
	t.Helper()
	require.Eventually(t, func() bool { return poster.postCount() == want }, 2*time.Second, time.Millisecond)
}

func awaitScheduled(t *testing.T, harness *queueHarness, want int) {
	t.Helper()
	require.Eventually(t, func() bool { return len(harness.scheduledDelays()) == want }, 2*time.Second, time.Millisecond)
}

func awaitReleased(t *testing.T, registration *raceRegistration) {
	t.Helper()
	require.Eventually(t, registration.released.Load, 2*time.Second, time.Millisecond,
		"the race's registration must be released after its last post")
}

// Test flow:
//  1. Settle an unsettled attempt through a poster that fails its first post, then fire the queue's timer.
//  2. Assert one post landed, a retry is scheduled 30 seconds out, and the race's registration stays held with one owed vote.
//  3. Advance the clock past the retry delay and fire the retry timer.
//  4. Assert the second post lands, the registration is released, no further retry is scheduled, and nothing is owed.
func TestAFailedRefusedVoteIsPostedAgainAfterTheFirstDelay(t *testing.T) {
	poster := &failingPoster{failures: alwaysFailing(1)}
	races, harness := retryEngine(t, poster)
	registration := races.admit()

	races.settle(race(unsettledAttempt()), nil, registration)
	harness.fire(0)
	awaitPosts(t, poster, 1)
	awaitScheduled(t, harness, 2)

	require.Equal(t, 30*time.Second, harness.scheduledDelays()[1], "the first retry waits the first delay")
	require.False(t, registration.released.Load(), "a race with a retry still owed keeps its registration")
	require.Eventually(t, func() bool { return races.OwedTimeoutVotes() == 1 }, 2*time.Second, time.Millisecond,
		"a scheduled retry is owed")

	harness.advance(30 * time.Second)
	harness.fire(1)
	awaitPosts(t, poster, 2)
	awaitReleased(t, registration)
	require.Len(t, harness.scheduledDelays(), 2, "a vote that landed is not retried")
	require.Eventually(t, func() bool { return races.OwedTimeoutVotes() == 0 }, 2*time.Second, time.Millisecond)
}

// Test flow:
//  1. For each table case's failure (the group judged the vote, the hosts dropped the escrow, an execution vote that belongs to the sweep), settle an outcome through a poster that fails with that error once, then fire the timer.
//  2. Assert one post landed, the registration was released, and no retry was scheduled — the failure is one a retry cannot change.
func TestAVoteARetryCannotChangeIsNotRetried(t *testing.T) {
	executionAttempt := unsettledAttempt()
	executionAttempt.ReceiptTime = testEpoch.Add(200 * time.Millisecond)
	cases := []struct {
		name          string
		attempt       AttemptOutcome
		escrowMissing bool
		failure       error
	}{
		{name: "the group judged the vote", attempt: unsettledAttempt(),
			failure: fmt.Errorf("inference 7: %w: insufficient votes", user.ErrTimeoutNotApplied)},
		{name: "the hosts dropped the escrow", attempt: unsettledAttempt(), escrowMissing: true,
			failure: fmt.Errorf("collect timeout votes: %w", errVoteCause)},
		{name: "an execution vote is the sweep's", attempt: executionAttempt,
			failure: fmt.Errorf("collect timeout votes: %w", errVoteCause)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			poster := &failingPoster{failures: []error{testCase.failure}}
			races, harness := retryEngine(t, poster)
			registration := races.admit()
			outcome := race(testCase.attempt)
			outcome.Lifecycle.EscrowMissing = testCase.escrowMissing

			races.settle(outcome, nil, registration)
			harness.fire(0)
			awaitPosts(t, poster, 1)
			awaitReleased(t, registration)

			require.Len(t, harness.scheduledDelays(), 1, "nothing is scheduled for a vote a retry cannot change")
			require.Eventually(t, func() bool { return races.OwedTimeoutVotes() == 0 }, 2*time.Second, time.Millisecond)
		})
	}
}

// Test flow:
//  1. Settle an unsettled attempt through a poster that always fails, and advance/fire the queue's timer for four retry rounds.
//  2. Assert each round still owes a retry and holds the registration.
//  3. Advance and fire a fifth time.
//  4. Assert the fifth post lands, the registration releases, the four retry delays doubled from 30s to 240s, nothing is owed, and releasing the registration again is a no-op.
func TestARefusedVoteIsRetriedFourTimesAtMost(t *testing.T) {
	poster := &failingPoster{failures: alwaysFailing(10)}
	races, harness := retryEngine(t, poster)
	registration := races.admit()

	races.settle(race(unsettledAttempt()), nil, registration)
	for round := range 4 {
		harness.advance(4 * time.Minute)
		harness.fire(round)
		awaitPosts(t, poster, round+1)
		awaitScheduled(t, harness, round+2)
		require.False(t, registration.released.Load(), "round %d still owes a retry", round)
	}
	harness.advance(4 * time.Minute)
	harness.fire(4)
	awaitPosts(t, poster, 5)
	awaitReleased(t, registration)

	delays := harness.scheduledDelays()
	require.Equal(t, []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second}, delays[1:])
	require.Eventually(t, func() bool { return races.OwedTimeoutVotes() == 0 }, 2*time.Second, time.Millisecond)
	require.NotPanics(t, registration.release, "a second release must be a no-op")
	races.Stop()
}

// Test flow:
//  1. Settle an unsettled attempt through a poster that always fails, fire the first post, and wait for the retry to be scheduled.
//  2. Call `Stop` on the engine and assert it returns promptly without waiting on the retry's timer.
//  3. Assert the waiting retry was posted immediately at `Stop`, no further retry was scheduled, and the registration was released.
func TestStopPostsAWaitingRetryAtOnceAndSchedulesNoMore(t *testing.T) {
	poster := &failingPoster{failures: alwaysFailing(10)}
	races, harness := retryEngine(t, poster)
	registration := races.admit()

	races.settle(race(unsettledAttempt()), nil, registration)
	harness.fire(0)
	awaitScheduled(t, harness, 2)

	stopped := make(chan struct{})
	go func() { races.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop waited on a retry's timer")
	}

	require.Equal(t, 2, poster.postCount(), "the waiting retry is posted once at Stop")
	require.Len(t, harness.scheduledDelays(), 2, "a stopped engine schedules no further retry")
	require.True(t, registration.released.Load())
}

// Test flow:
//  1. Post the timeout plan for an unsettled attempt through a poster that fails once, collecting emitted events.
//  2. Assert the failed vote is handed back as one retry for the next round.
//  3. Assert exactly a started event followed by a failed event were reported.
func TestARetryRoundReportsItsOwnStartedAndFinishedEvents(t *testing.T) {
	poster := &failingPoster{failures: alwaysFailing(1)}
	steps := race(unsettledAttempt()).TimeoutPlan()
	var events []TimeoutEvent

	retries := postTimeouts(context.Background(), poster, steps, false, func(event TimeoutEvent) { events = append(events, event) })

	require.Len(t, retries, 1, "a failed refused vote is handed back for the next round")
	require.Len(t, events, 2)
	require.Equal(t, TimeoutActionStarted, events[0].Action)
	require.Equal(t, TimeoutActionFailed, events[1].Action)
}

type unjudgedPoster struct {
	failingPoster
}

func (p *unjudgedPoster) SettleTimeout(ctx context.Context, step TimeoutStep) (TimeoutVote, error) {
	if p.postCount() == 0 {
		p.mu.Lock()
		p.posts++
		p.mu.Unlock()
		return TimeoutVote{Kind: TimeoutKindRefused, Unjudged: true}, fmt.Errorf("inference 7: %w: insufficient votes", user.ErrTimeoutNotApplied)
	}
	return p.failingPoster.SettleTimeout(ctx, step)
}

// Test flow:
//  1. Settle an unsettled attempt through a poster whose first vote came back unjudged — the verifiers erred, so the group never weighed it — then fire the timer.
//  2. Assert a retry is scheduled the first delay out: an unjudged vote is not the group's answer, however the session wrapped it.
func TestAnUnjudgedRefusedVoteIsPostedAgain(t *testing.T) {
	poster := &unjudgedPoster{}
	races, harness := retryEngine(t, poster)
	registration := races.admit()

	races.settle(race(unsettledAttempt()), nil, registration)
	harness.fire(0)
	awaitPosts(t, &poster.failingPoster, 1)
	awaitScheduled(t, harness, 2)

	require.Equal(t, 30*time.Second, harness.scheduledDelays()[1], "the retry waits the first delay")
}

type outcomeHandler struct {
	outcome string
	detail  string
}

func (h outcomeHandler) HandleTimeout(context.Context, uint64, time.Time, *host.InferencePayload) (user.TimeoutResult, error) {
	return user.TimeoutResult{Reason: TimeoutKindRefused, Outcome: h.outcome, DetailReason: h.detail}, fmt.Errorf("inference 7: %w", user.ErrTimeoutNotApplied)
}

func (outcomeHandler) HandleErrorMiss(context.Context, uint64, []byte, []byte) (user.TimeoutResult, error) {
	return user.TimeoutResult{}, nil
}

func (outcomeHandler) FinishTxFor(uint64) []byte { return nil }

func (outcomeHandler) TimeoutDeadline(uint64, time.Time) (string, time.Time) { return "", time.Time{} }

// Test flow:
//  1. Settle a vote through session timeouts whose handler ended it as a failed vote collection, a diff that landed without the timeout, then a weighed but short vote.
//  2. Assert only the failed collection reads as unjudged: the other two are the group's answer.
func TestSessionTimeoutsMarkAVoteTheGroupNeverWeighedAsUnjudged(t *testing.T) {
	for _, testCase := range []struct {
		outcome      string
		detail       string
		wantUnjudged bool
	}{
		{user.TimeoutOutcomeVoteCollectionFailed, "deadline_exceeded", true},
		{"diff_send_failed", "timeout_not_applied", false},
		{"insufficient_votes", "vote_weight_short", false},
	} {
		t.Run(testCase.outcome, func(t *testing.T) {
			timeouts := &SessionTimeouts{handler: outcomeHandler{outcome: testCase.outcome, detail: testCase.detail}}

			vote, _ := timeouts.SettleTimeout(context.Background(), TimeoutStep{Nonce: 7})

			if vote.Unjudged != testCase.wantUnjudged {
				t.Fatalf("Unjudged = %v, want %v", vote.Unjudged, testCase.wantUnjudged)
			}
		})
	}
}
