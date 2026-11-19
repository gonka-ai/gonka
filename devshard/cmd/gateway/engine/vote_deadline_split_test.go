package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	refusalVoteDue   = 65 * time.Second
	executionVoteDue = 32 * time.Minute
	refusedNonce     = 1
	executedNonce    = 2
)

type deadlinePoster struct {
	failingPoster
	settledAt time.Time
	dueIn     map[uint64]time.Duration
}

func (p *deadlinePoster) VoteDeadline(nonce uint64, _ time.Time) time.Time {
	return p.settledAt.Add(p.dueIn[nonce])
}

func raceOwingTwoDeadlines() RaceOutcome {
	refused, executed := unsettledAttempt(), unsettledAttempt()
	refused.Nonce, executed.Nonce = refusedNonce, executedNonce
	outcome := race(refused)
	outcome.Attempts = append(outcome.Attempts, executed)
	return outcome
}

// Test flow:
//  1. Settle a race that owes a refusal vote due in 65 seconds and an execution vote due in 32 minutes.
//  2. Read the timers the vote queue armed.
//  3. Assert each vote waits on a timer of its own deadline: one task armed at the earliest would post the later vote early and hold a posting place until it falls due.
func TestEachVoteDeadlineOfARaceWaitsOnItsOwnTimer(t *testing.T) {
	poster := &deadlinePoster{dueIn: map[uint64]time.Duration{refusedNonce: refusalVoteDue, executedNonce: executionVoteDue}}
	races, harness := retryEngine(t, poster)
	poster.settledAt = harness.now()

	races.settle(raceOwingTwoDeadlines(), nil, races.admit())

	require.ElementsMatch(t, []time.Duration{refusalVoteDue, executionVoteDue}, harness.scheduledDelays())
}

// Test flow:
//  1. Settle a race that owes votes at two deadlines, and fire the earlier one's timer.
//  2. Assert only the refusal vote was posted and the race keeps its registration: its later vote is still owed.
//  3. Fire the later timer and assert the execution vote is posted and the registration released.
func TestARaceKeepsItsRegistrationUntilItsLastDeadlineIsPosted(t *testing.T) {
	poster := &deadlinePoster{dueIn: map[uint64]time.Duration{refusedNonce: refusalVoteDue, executedNonce: executionVoteDue}}
	races, harness := retryEngine(t, poster)
	poster.settledAt = harness.now()
	registration := races.admit()

	races.settle(raceOwingTwoDeadlines(), nil, registration)
	awaitScheduled(t, harness, 2)
	harness.advance(refusalVoteDue)
	harness.fire(0)
	awaitPosts(t, &poster.failingPoster, 1)
	require.False(t, registration.released.Load(), "a race with a later vote still owed keeps its registration")

	harness.advance(executionVoteDue)
	harness.fire(1)
	awaitPosts(t, &poster.failingPoster, 2)
	awaitReleased(t, registration)
}
