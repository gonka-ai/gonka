package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

// recordingJournal keeps the steps a coordinator reported.
type recordingJournal struct {
	mu    sync.Mutex
	steps []RaceStep
}

func (r *recordingJournal) RecordStep(step RaceStep) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step)
}

func (r *recordingJournal) HostDeniedCrown(string, string, int) {}

func (r *recordingJournal) HostCrownedAgain(string, string) {}

func (r *recordingJournal) recorded() []RaceStep {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RaceStep(nil), r.steps...)
}

// Test flow:
//  1. Complete an attempt with a widest-shaped finished outcome through a `pausedCoordinator` recording into a `recordingJournal`.
//  2. Mutate the outcome's `ContentChunks` field after reporting.
//  3. Assert the journal recorded one `RaceStepAttemptFinished` step carrying a snapshot equal to the outcome before the mutation.
func TestAFinishedAttemptIsReportedAsACopyOfWhatItDelivered(t *testing.T) {
	steps := &recordingJournal{}
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.Journal = steps
	delivered := widestFinishedOutcome()
	delivered.Terminal = TerminalLost
	attempt := &liveAttempt{nonce: 77, participant: "host-3", cancel: func() {}}
	coordinator := pausedCoordinator(fixture, 1, attempt)
	coordinator.escrowID = "escrow-1"

	coordinator.complete(attempt, AttemptEvent{Kind: AttemptDone, Nonce: 77, At: testEpoch, Outcome: &delivered})
	attempt.outcome.ContentChunks = 0

	want := widestFinishedOutcome()
	want.Terminal = TerminalLost
	require.Equal(t, []RaceStep{{
		Kind: RaceStepAttemptFinished, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 77,
		Participant: "host-3", Terminal: TerminalLost, HasOutcome: true, Outcome: want,
	}}, steps.recorded())
}

// Test flow:
//  1. Hand off the winner of a two-attempt race, then apply a failed pick carrying either a scheduler refusal or the race's own cancellation error.
//  2. Assert a scheduler refusal after hand-off is traced as a `RaceStepEscalationUnfilled` step.
//  3. Assert the race's own cancellation is traced as nothing at all.
func TestAFailedPickIsTracedUnlessTheRaceCancelledIt(t *testing.T) {
	testCases := []struct {
		name    string
		pickErr error
		want    []RaceStep
	}{
		{
			name:    "a scheduler refusal after the hand-off",
			pickErr: scheduler.ErrNoAvailableHost,
			want: []RaceStep{{
				Kind: RaceStepEscalationUnfilled, RequestID: "request-1", EscrowID: "escrow-1",
				Reason: "receipt_timeout", Attempts: 2, Err: scheduler.ErrNoAvailableHost,
			}},
		},
		{name: "the race's own cancellation", pickErr: context.Canceled},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			steps := &recordingJournal{}
			fixture := newRaceFixture(settledPolicy(), 2)
			fixture.deps.Journal = steps
			winner := &liveAttempt{nonce: 90, participant: "host-0", done: true, cancel: func() {}}
			loser := &liveAttempt{nonce: 91, participant: "host-1", cancel: func() {}}
			coordinator := pausedCoordinator(fixture, 2, winner, loser)
			coordinator.escrowID, coordinator.winner = "escrow-1", winner
			require.True(t, coordinator.release(), "the served winner was not handed off")
			coordinator.pickCancel, coordinator.pickReason = func() {}, "receipt_timeout"

			coordinator.applyPick(pickedHost{err: testCase.pickErr})

			require.Equal(t, testCase.want, steps.recorded())
		})
	}
}

// Test flow:
//  1. Start a paused coordinator with one attempt in flight and a speculative pick outstanding on its escrow.
//  2. Apply the pick's answer: the pinned escrow cannot pay this attempt.
//  3. Assert the race still holds its one attempt in flight, launched nothing, and traced the escalation as unfilled with that error.
func TestAPinnedEscrowTooShortForAHedgeLeavesTheRaceRunning(t *testing.T) {
	steps := &recordingJournal{}
	fixture := newRaceFixture(settledPolicy(), 2)
	fixture.deps.Journal = steps
	running := &liveAttempt{nonce: 90, participant: "host-0", cancel: func() {}}
	coordinator := pausedCoordinator(fixture, 2, running)
	coordinator.escrowID = "escrow-1"
	coordinator.pickCancel, coordinator.pickReason = func() {}, "first_token_floor"
	short := fmt.Errorf("%w: %w", scheduler.ErrPinnedEscrowShort, types.ErrInsufficientBalance)

	coordinator.applyPick(pickedHost{err: short})

	require.Len(t, coordinator.attempts, 1, "a declined hedge launches nothing")
	require.Equal(t, 1, coordinator.pending, "the attempt already running keeps the race alive")
	require.Equal(t, []RaceStep{{
		Kind: RaceStepEscalationUnfilled, RequestID: "request-1", EscrowID: "escrow-1",
		Reason: "first_token_floor", Attempts: 1, Err: short,
	}}, steps.recorded())
}
