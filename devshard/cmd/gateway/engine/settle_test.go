package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/logging"
	"devshard/user"
)

type recordedPost struct {
	nonce  uint64
	sentAt time.Time
}

type stubPoster struct {
	posts  []recordedPost
	vote   string
	detail string
	err    error
}

func (p *stubPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *stubPoster) SettleTimeout(_ context.Context, step TimeoutStep) (TimeoutVote, error) {
	p.posts = append(p.posts, recordedPost{nonce: step.Nonce, sentAt: step.StartedAt})
	return TimeoutVote{Kind: p.vote, Detail: p.detail}, p.err
}

func unsettledAttempt() AttemptOutcome {
	attempt := failedAttempt(TerminalNoReceipt)
	attempt.ReceiptTime = time.Time{}
	attempt.FirstToken = time.Time{}
	return attempt
}

func settleEvents(outcome RaceOutcome, poster TimeoutPoster) []TimeoutEvent {
	var events []TimeoutEvent
	settleSteps(context.Background(), poster, outcome.TimeoutPlan(), outcome.Lifecycle.EscrowMissing, func(event TimeoutEvent) { events = append(events, event) })
	return events
}

// countingPoster records how many events had been reported when each vote was posted.
type countingPoster struct {
	reported   *[]TimeoutEvent
	seenAtPost []int
}

func (p *countingPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *countingPoster) SettleTimeout(context.Context, TimeoutStep) (TimeoutVote, error) {
	p.seenAtPost = append(p.seenAtPost, len(*p.reported))
	return TimeoutVote{Kind: TimeoutKindRefused}, nil
}

// Test flow:
//  1. Build a race outcome with two unsettled attempts and settle it through a `countingPoster` that records how many events had already been reported at each post.
//  2. Assert the first post saw one reported event and the second post saw three.
//  3. Assert both attempts' first reported event is a started action.
func TestAStartedEventIsReportedBeforeItsVoteIsPosted(t *testing.T) {
	first := unsettledAttempt()
	first.Nonce = 11
	second := unsettledAttempt()
	second.Nonce = 12
	outcome := race(first)
	outcome.Attempts = []AttemptOutcome{first, second}
	var reported []TimeoutEvent
	poster := &countingPoster{reported: &reported}

	settleSteps(context.Background(), poster, outcome.TimeoutPlan(), outcome.Lifecycle.EscrowMissing, func(event TimeoutEvent) { reported = append(reported, event) })

	require.Equal(t, []int{1, 3}, poster.seenAtPost)
	require.Equal(t, TimeoutActionStarted, reported[0].Action)
	require.Equal(t, TimeoutActionStarted, reported[2].Action)
}

// Test flow:
//  1. Settle a race with one unsettled attempt through a `stubPoster` that votes refused.
//  2. Assert exactly one post landed, for the attempt's own nonce, sent at its `StartedAt`.
//  3. Assert two events were reported: started then completed.
func TestTimeoutLadderPostsWhenNoSkipConditionHolds(t *testing.T) {
	poster := &stubPoster{vote: "refused"}

	events := settleEvents(race(unsettledAttempt()), poster)

	if len(poster.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(poster.posts))
	}
	if poster.posts[0].nonce != 7 || !poster.posts[0].sentAt.Equal(testEpoch) {
		t.Fatalf("post = %+v, want nonce 7 waited out from the record's StartedAt", poster.posts[0])
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want a started and a completed event", events)
	}
	if events[0].Action != TimeoutActionStarted || events[1].Action != TimeoutActionCompleted {
		t.Fatalf("actions = %q then %q, want started then completed", events[0].Action, events[1].Action)
	}
}

// Test flow:
//  1. For each table case's attempt shape (no skip condition, phase transition aborted, an already-finished empty/error/capability-refused stream, a long response past/just-under its exemption — where `ContentSource` is what marks a stream as carrying real content, since `ContentChunks` alone also counts error events — or a state-divergent host), settle it and check whether a vote posted and what reason the first event carries.
//  2. For a separately tabled case of an attempt whose nonce already finished successfully, assert it produces neither a post nor any event at all.
func TestTimeoutLadderSkipConditions(t *testing.T) {
	longResponse := unsettledAttempt()
	longResponse.ContentChunks = 3
	longResponse.ContentSource = "delta.content"
	longResponse.Completed = testEpoch.Add(longResponseExemption)

	justUnderLongResponse := unsettledAttempt()
	justUnderLongResponse.ContentChunks = 3
	justUnderLongResponse.ContentSource = "delta.content"
	justUnderLongResponse.Completed = testEpoch.Add(longResponseExemption - time.Nanosecond)

	stateDivergent := unsettledAttempt()
	stateDivergent.StateDivergent = true

	phaseAborted := unsettledAttempt()
	phaseAborted.PhaseTransitionAborted = true

	emptyFinished := unsettledAttempt()
	emptyFinished.Terminal = TerminalEmptyStream
	emptyFinished.ReceiptTime = testEpoch.Add(time.Second)
	emptyFinished.NonceFinished = true

	errorStreamFinished := unsettledAttempt()
	errorStreamFinished.Terminal = TerminalErrorStream
	errorStreamFinished.ErrorSource = "error"
	errorStreamFinished.NonceFinished = true

	capabilityRefusedFinished := unsettledAttempt()
	capabilityRefusedFinished.Terminal = TerminalCapabilityRefused
	capabilityRefusedFinished.NonceFinished = true

	won := cleanAttempt()

	testCases := []struct {
		name       string
		attempt    AttemptOutcome
		wantPosted bool
		wantReason string
	}{
		{name: "no_skip_condition", attempt: unsettledAttempt(), wantPosted: true, wantReason: TimeoutReasonNone},
		{name: "phase_transition_aborted", attempt: phaseAborted, wantReason: TimeoutReasonPhaseAborted},
		{name: "empty_stream_already_finished", attempt: emptyFinished, wantReason: TimeoutReasonEmptyStream},
		{name: "error_stream_already_finished", attempt: errorStreamFinished, wantReason: TimeoutReasonNonceFinished},
		{name: "capability_refusal_already_finished", attempt: capabilityRefusedFinished, wantReason: TimeoutReasonNonceFinished},
		{name: "long_response_after_content", attempt: longResponse, wantReason: TimeoutReasonLongResponse},
		{name: "just_under_long_response_exemption", attempt: justUnderLongResponse, wantPosted: true, wantReason: TimeoutReasonNone},
		{name: "state_divergent_host_is_still_settled", attempt: stateDivergent, wantPosted: true, wantReason: TimeoutReasonNone},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			poster := &stubPoster{}

			events := settleEvents(race(testCase.attempt), poster)

			if posted := len(poster.posts) == 1; posted != testCase.wantPosted {
				t.Fatalf("posted = %v, want %v", posted, testCase.wantPosted)
			}
			if len(events) == 0 {
				t.Fatalf("events = %+v, want at least one", events)
			}
			if events[0].Reason != testCase.wantReason {
				t.Fatalf("reason = %q, want %q", events[0].Reason, testCase.wantReason)
			}
		})
	}

	for _, testCase := range []struct {
		name    string
		attempt AttemptOutcome
	}{
		{name: "attempt_that_finished_its_nonce", attempt: won},
	} {
		t.Run(testCase.name+"_is_not_in_the_plan", func(t *testing.T) {
			t.Parallel()
			poster := &stubPoster{}

			events := settleEvents(race(testCase.attempt), poster)

			if len(poster.posts) != 0 {
				t.Fatalf("posts = %d, want 0", len(poster.posts))
			}
			if len(events) != 0 {
				t.Fatalf("events = %+v, want none", events)
			}
		})
	}
}

// Test flow:
//  1. Build an attempt that matches several skip conditions at once (phase aborted, an already-finished empty stream, and a long response past its exemption).
//  2. Settle it and assert the reported reason is the first matching rung of the ladder, phase-aborted, not any of the later ones.
func TestTimeoutSkipLadderOrder(t *testing.T) {
	attempt := unsettledAttempt()
	attempt.PhaseTransitionAborted = true
	attempt.Terminal = TerminalEmptyStream
	attempt.ReceiptTime = testEpoch.Add(time.Second)
	attempt.NonceFinished = true
	attempt.ContentChunks = 3
	attempt.Completed = testEpoch.Add(longResponseExemption)

	events := settleEvents(race(attempt), &stubPoster{})

	if events[0].Reason != TimeoutReasonPhaseAborted {
		t.Fatalf("reason = %q, want the first matching rung %q", events[0].Reason, TimeoutReasonPhaseAborted)
	}
}

// Test flow:
//  1. For each table case's attempt (one that never received a receipt, one that received a receipt but then failed), settle it.
//  2. Assert the reported timeout kind is refused for the unreceipted attempt and execution for the receipted one.
func TestTimeoutKindFollowsReceipt(t *testing.T) {
	received := unsettledAttempt()
	received.ReceiptTime = testEpoch.Add(300 * time.Millisecond)

	testCases := []struct {
		name     string
		attempt  AttemptOutcome
		wantKind string
	}{
		{name: "never_receipted", attempt: unsettledAttempt(), wantKind: TimeoutKindRefused},
		{name: "receipted_then_failed", attempt: received, wantKind: TimeoutKindExecution},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			events := settleEvents(race(testCase.attempt), &stubPoster{})

			if events[0].Kind != testCase.wantKind {
				t.Fatalf("kind = %q, want %q", events[0].Kind, testCase.wantKind)
			}
		})
	}
}

// Test flow:
//  1. Settle an unsettled attempt through a `stubPoster` that votes an execution kind.
//  2. Assert the started event still reports the refused kind guessed before posting.
//  3. Assert the completed event's kind is overridden to the kind the poster actually voted.
func TestTimeoutVoteOverridesKind(t *testing.T) {
	poster := &stubPoster{vote: "execution"}

	events := settleEvents(race(unsettledAttempt()), poster)

	if events[0].Kind != TimeoutKindRefused {
		t.Fatalf("started kind = %q, want %q", events[0].Kind, TimeoutKindRefused)
	}
	if events[1].Kind != TimeoutKindExecution {
		t.Fatalf("completed event = %+v, want the posted vote to set the kind", events[1])
	}
}

// Test flow:
//  1. Settle an unsettled attempt through a `stubPoster` that votes refused but fails to post.
//  2. Assert the completed event reports a failed action with the collection-error reason.
func TestTimeoutPostFailureIsReported(t *testing.T) {
	poster := &stubPoster{vote: "refused", err: errors.New("collect timeout votes")}

	events := settleEvents(race(unsettledAttempt()), poster)

	if events[1].Action != TimeoutActionFailed || events[1].Reason != TimeoutReasonCollectionError {
		t.Fatalf("failure event = %+v, want a failed action with the collection reason", events[1])
	}
}

// Test flow:
//  1. Build a race outcome with two unsettled attempts and one already-settled attempt.
//  2. Settle it through a `stubPoster`.
//  3. Assert exactly one post landed per unsettled nonce, and the settled attempt's nonce was never posted.
func TestEveryUnsettledNonceIsPostedOnce(t *testing.T) {
	first := unsettledAttempt()
	first.Nonce = 11
	second := unsettledAttempt()
	second.Nonce = 12
	second.Participant = "gonka1other"
	settled := cleanAttempt()
	settled.Nonce = 13
	outcome := race(first)
	outcome.Attempts = []AttemptOutcome{first, second, settled}

	poster := &stubPoster{}
	settleEvents(outcome, poster)

	if len(poster.posts) != 2 {
		t.Fatalf("posts = %+v, want one per unsettled nonce", poster.posts)
	}
	if poster.posts[0].nonce != 11 || poster.posts[1].nonce != 12 {
		t.Fatalf("posts = %+v, want nonces 11 and 12", poster.posts)
	}
}

// Test flow:
//  1. Settle a race with one unsettled attempt.
//  2. Assert the reported event carries the attempt's participant, the race's model, and the attempt's nonce.
func TestTimeoutEventCarriesRaceIdentity(t *testing.T) {
	events := settleEvents(race(unsettledAttempt()), &stubPoster{})

	if events[0].Participant != testParticipant || events[0].Model != testModel || events[0].Nonce != 7 {
		t.Fatalf("event = %+v, want the attempt's participant, the race model and the nonce", events[0])
	}
}

// Test flow:
//  1. Build an attempt that ended in an error stream, whose only chunk was the error event itself, held open past the long-response exemption.
//  2. Compute its `TimeoutPlan`.
//  3. Assert the plan still posts a timeout vote for the unfinished nonce, since the long-response exemption does not apply to a stream with no real content.
func TestAnErrorOnlyStreamStillVotesItsTimeout(t *testing.T) {
	attempt := failedAttempt(TerminalErrorStream)
	attempt.ContentChunks = 1
	attempt.ContentSource = ""
	attempt.Completed = attempt.StartedAt.Add(longResponseExemption)
	outcome := RaceOutcome{Attempts: []AttemptOutcome{attempt}}

	plan := outcome.TimeoutPlan()

	if len(plan) != 1 {
		t.Fatalf("plan has %d steps, want the unfinished nonce in it", len(plan))
	}
	if !plan[0].Post {
		t.Fatal("timeout vote skipped: the nonce is committed, unfinished and now unreclaimable")
	}
}

// Test flow:
//  1. Build an attempt truncated after streaming real content past the long-response exemption.
//  2. Compute its `TimeoutPlan`.
//  3. Assert the plan reports the attempt but does not post — the long-response exemption still holds.
func TestALongRunningContentStreamKeepsItsExemption(t *testing.T) {
	attempt := failedAttempt(TerminalStreamTruncated)
	attempt.ContentChunks = 40
	attempt.ContentSource = "delta.content"
	attempt.Completed = attempt.StartedAt.Add(longResponseExemption)
	outcome := RaceOutcome{Attempts: []AttemptOutcome{attempt}}

	plan := outcome.TimeoutPlan()

	if len(plan) != 1 || plan[0].Post {
		t.Fatalf("a long content stream lost its exemption: %+v", plan)
	}
}

// Test flow:
//  1. Settle an unsettled attempt through a `stubPoster` that fails posting with `user.ErrNonceFinishedWhileWaiting`.
//  2. Assert the completed event reports a skipped action with the nonce-finished reason, not a failure.
func TestANonceFinishedWhileWaitingIsSkippedRatherThanFailed(t *testing.T) {
	poster := &stubPoster{vote: "execution", err: user.ErrNonceFinishedWhileWaiting}

	events := settleEvents(race(unsettledAttempt()), poster)

	if events[1].Action != TimeoutActionSkipped || events[1].Reason != TimeoutReasonNonceFinished {
		t.Fatalf("event = %+v, want skipped/%s", events[1], TimeoutReasonNonceFinished)
	}
}

// Test flow:
//  1. For each table case's poster detail and error (a named verifier failure, an unnamed collection failure, a named short vote, an unnamed unapplied timeout), settle an outcome through a `stubPoster` returning that failure.
//  2. Assert the final reported event's reason matches the case's expected reason, falling back to the generic reason only when the poster named none.
func TestSettleTimeoutsCarriesTheVerifierFailureItWasGiven(t *testing.T) {
	tests := []struct {
		name       string
		detail     string
		err        error
		wantReason string
	}{
		{"a named verifier failure", "verifier_unreachable", errors.New("collect"), "verifier_unreachable"},
		{"an unnamed collection failure", "", errors.New("collect"), TimeoutReasonCollectionError},
		{"a named short vote", "vote_weight_short", user.ErrTimeoutNotApplied, "vote_weight_short"},
		{"an unnamed unapplied timeout", "", user.ErrTimeoutNotApplied, TimeoutReasonNotApplied},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			poster := &stubPoster{vote: "refused", detail: testCase.detail, err: testCase.err}
			outcome := RaceOutcome{EscrowID: "escrow-1", Attempts: []AttemptOutcome{unsettledAttempt()}}

			events := settleEvents(outcome, poster)

			posted := events[len(events)-1]
			if posted.Reason != testCase.wantReason {
				t.Errorf("reason = %q, want %q", posted.Reason, testCase.wantReason)
			}
		})
	}
}

// Test flow:
//  1. Settle a race with one unsettled attempt.
//  2. Assert both the started and completed events carry the race's request id.
func TestEveryTimeoutEventNamesTheRequestThatOwedIt(t *testing.T) {
	events := settleEvents(race(unsettledAttempt()), &stubPoster{})

	require.Len(t, events, 2)
	require.Equal(t, "req-1", events[0].RequestID)
	require.Equal(t, "req-1", events[1].RequestID)
}

// Test flow:
//  1. Build a settle context for a named request id.
//  2. Assert `logging.RequestID` reads the same request id back out of it.
func TestTheSettleContextCarriesTheRequestIntoTheSharedStages(t *testing.T) {
	requestID, carried := logging.RequestID(settleContext("req-9"))

	require.True(t, carried)
	require.Equal(t, "req-9", requestID)
}

// Test flow:
//  1. Build a settle context for an empty request id.
//  2. Assert `logging.RequestID` reports no request id carried.
func TestTheSettleContextOfAnUnnamedRaceCarriesNoRequest(t *testing.T) {
	_, carried := logging.RequestID(settleContext(""))

	require.False(t, carried)
}
