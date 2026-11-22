package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

type pacedHandler struct {
	deadline   time.Time
	steps      []string
	notReady   bool
	waitsLeft  int
	voteResult user.TimeoutResult
	voteErr    error
}

func (h *pacedHandler) WaitTimeoutDeadline(context.Context, uint64, time.Time) (types.TimeoutReason, bool, user.TimeoutResult, error) {
	h.steps = append(h.steps, "wait")
	if h.notReady {
		return types.TimeoutReason_TIMEOUT_REASON_UNSPECIFIED, false, user.TimeoutResult{Outcome: "skipped"}, nil
	}
	return types.TimeoutReason_TIMEOUT_REASON_REFUSED, true, user.TimeoutResult{}, nil
}

func (h *pacedHandler) VoteTimeout(context.Context, uint64, time.Time, *host.InferencePayload, types.TimeoutReason) (user.TimeoutResult, bool, error) {
	h.steps = append(h.steps, "vote")
	if h.waitsLeft > 0 {
		h.waitsLeft--
		return user.TimeoutResult{}, true, nil
	}
	return h.voteResult, false, h.voteErr
}

func (h *pacedHandler) HandleErrorMiss(context.Context, uint64, []byte, []byte) (user.TimeoutResult, error) {
	return user.TimeoutResult{}, nil
}

func (h *pacedHandler) FinishTxFor(uint64) []byte { return nil }

func (h *pacedHandler) TimeoutDeadline(uint64, time.Time) (string, time.Time) {
	return string(TimeoutKindRefused), h.deadline
}

func pacedStep(handler *pacedHandler, pace func(context.Context) error) (TimeoutVote, error) {
	return (&SessionTimeouts{handler: handler}).SettleTimeout(context.Background(), TimeoutStep{Nonce: 3, Post: true, Pace: pace})
}

// Test flow:
//  1. Settle a step carrying a pace through a handler that votes at once, with a pace that records its own call.
//  2. Assert the handler waited for the deadline, then the pace ran, then the vote was cast.
func TestAPacedStepWaitsForTheDeadlineThenPacesThenVotes(t *testing.T) {
	handler := &pacedHandler{voteResult: user.TimeoutResult{Applied: true}}
	pace := func(context.Context) error {
		handler.steps = append(handler.steps, "pace")
		return nil
	}

	vote, err := pacedStep(handler, pace)

	if err != nil || vote.Kind != "" {
		t.Fatalf("SettleTimeout() = %+v, %v, want an applied vote", vote, err)
	}
	if want := []string{"wait", "pace", "vote"}; !slices.Equal(handler.steps, want) {
		t.Fatalf("steps = %v, want %v", handler.steps, want)
	}
}

// Test flow:
//  1. Settle a paced step through a handler whose vote says the deadline moved, as a recovered receipt does.
//  2. Assert the step comes back deferred with no error, and the pace and the vote each ran once with no second wait: the execution window is left to the queue's timer.
func TestAVoteWhoseDeadlineMovedIsDeferredInsteadOfWaitedOut(t *testing.T) {
	handler := &pacedHandler{waitsLeft: 1, voteResult: user.TimeoutResult{Applied: true}}
	pace := func(context.Context) error {
		handler.steps = append(handler.steps, "pace")
		return nil
	}

	vote, err := pacedStep(handler, pace)

	if err != nil || !vote.Deferred {
		t.Fatalf("SettleTimeout() = %+v, %v, want a deferred vote and no error", vote, err)
	}
	if want := []string{"wait", "pace", "vote"}; !slices.Equal(handler.steps, want) {
		t.Fatalf("steps = %v, want %v", handler.steps, want)
	}
}

// Test flow:
//  1. Settle a paced step through a handler whose wait ends without a vote due.
//  2. Assert the pace never ran and the vote was never cast, so a skipped nonce takes no pacing slot.
func TestAPacedStepThatIsNotDueTakesNoPacingSlot(t *testing.T) {
	handler := &pacedHandler{notReady: true}
	paced := false

	_, err := pacedStep(handler, func(context.Context) error {
		paced = true
		return nil
	})

	if err != nil || paced {
		t.Fatalf("SettleTimeout() error = %v, paced = %v, want neither", err, paced)
	}
	if want := []string{"wait"}; !slices.Equal(handler.steps, want) {
		t.Fatalf("steps = %v, want %v", handler.steps, want)
	}
}

// Test flow:
//  1. Settle a paced step whose pace fails with an interrupted wait.
//  2. Assert the settle returns that error and the vote was never cast.
func TestAPacedStepWhosePaceIsInterruptedCastsNoVote(t *testing.T) {
	handler := &pacedHandler{}
	interrupted := errors.New("pacing interrupted")

	_, err := pacedStep(handler, func(context.Context) error { return interrupted })

	if !errors.Is(err, interrupted) {
		t.Fatalf("SettleTimeout() = %v, want %v", err, interrupted)
	}
	if want := []string{"wait"}; !slices.Equal(handler.steps, want) {
		t.Fatalf("steps = %v, want %v", handler.steps, want)
	}
}

// Test flow:
//  1. Settle a paced step through a handler whose deadline is still an hour ahead.
//  2. Assert the step comes back deferred with no error and the handler neither waited, paced nor voted: the queue's timer holds the vote, not a sleep.
func TestAStepWhoseDeadlineIsStillAheadIsDeferredWithoutWaiting(t *testing.T) {
	handler := &pacedHandler{deadline: time.Now().Add(time.Hour)}

	vote, err := pacedStep(handler, func(context.Context) error {
		handler.steps = append(handler.steps, "pace")
		return nil
	})

	if err != nil || !vote.Deferred {
		t.Fatalf("SettleTimeout() = %+v, %v, want a deferred vote and no error", vote, err)
	}
	if len(handler.steps) != 0 {
		t.Fatalf("steps = %v, want none", handler.steps)
	}
}

// Test flow:
//  1. Settle a step carrying no pace through the same handler.
//  2. Assert the handler waited and voted once with no pace between them.
func TestAStepWithoutAPaceWaitsAndVotesOnce(t *testing.T) {
	handler := &pacedHandler{}

	_, err := pacedStep(handler, nil)

	if err != nil {
		t.Fatalf("SettleTimeout() = %v, want nil", err)
	}
	if want := []string{"wait", "vote"}; !slices.Equal(handler.steps, want) {
		t.Fatalf("steps = %v, want %v", handler.steps, want)
	}
}
