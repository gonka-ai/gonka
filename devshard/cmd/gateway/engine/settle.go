package engine

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"time"

	"devshard/logging"
	"devshard/user"
)

// TimeoutPoster posts one nonce's vote and says when the protocol will accept it, so the queue can hold
// the vote without a goroutine sleeping for it. See README, "Timeout votes".
type TimeoutPoster interface {
	SettleTimeout(ctx context.Context, step TimeoutStep) (TimeoutVote, error)
	VoteDeadline(nonce uint64, startedAt time.Time) time.Time
}

type TimeoutVote struct {
	Kind          string
	Detail        string
	Unjudged      bool
	VerifyRejects []string
	Completeness  MissProofCompleteness
}

type TimeoutEvent struct {
	RequestID     string
	EscrowID      string
	Participant   string
	Model         string
	Nonce         uint64
	Kind          string
	Action        string
	Reason        string
	VerifyRejects []string
	Completeness  MissProofCompleteness
}

// SettleKind is how a nonce is settled. See ../docs/race.md, "Error misses".
type SettleKind int

const (
	SettleByVote SettleKind = iota
	SettleByMissClaim
)

// TimeoutStep is one nonce's vote; StartedAt is the race's start, the stamp every nonce it commits carries, not the attempt's dispatch. See README, "Timeout votes".
type TimeoutStep struct {
	Nonce     uint64
	StartedAt time.Time
	Post      bool
	Kind      SettleKind
	Proof     *MissProof
	Event     TimeoutEvent
}

func timeoutKind(a AttemptOutcome) string {
	if a.claimsMiss() {
		return TimeoutKindErrorMiss
	}
	if a.ReceiptTime.IsZero() {
		return TimeoutKindRefused
	}
	return TimeoutKindExecution
}

func timeoutVoteKind(vote, fallback string) string {
	switch vote {
	case TimeoutKindRefused, TimeoutKindExecution:
		return vote
	}
	return fallback
}

func (o RaceOutcome) nonceSettled(a AttemptOutcome) bool {
	return a.NonceFinished && !a.emptyStream() && !a.errorStream()
}

// timeoutSkipReason names every skip; a diverged escrow state is deliberately not one. See race.md, "Timeout votes".
func (o RaceOutcome) timeoutSkipReason(a AttemptOutcome) (string, bool) {
	if a.claimsMiss() {
		return "", false
	}
	switch {
	case a.PhaseTransitionAborted:
		return TimeoutReasonPhaseAborted, true
	case a.emptyStream() && a.NonceFinished:
		return TimeoutReasonEmptyStream, true
	case a.NonceFinished:
		return TimeoutReasonNonceFinished, true
	case o.longResponseExempt(a):
		return TimeoutReasonLongResponse, true
	}
	return "", false
}

func (o RaceOutcome) releaseHeldFinishes() {
	for _, attempt := range o.Attempts {
		attempt.MissProof.releaseHeldFinish()
	}
}

func (o RaceOutcome) TimeoutPlan() []TimeoutStep {
	steps := make([]TimeoutStep, 0, len(o.Attempts))
	for _, attempt := range o.Attempts {
		if o.nonceSettled(attempt) {
			continue
		}
		step := TimeoutStep{
			Nonce:     attempt.Nonce,
			StartedAt: attempt.StartedAt,
			Event: TimeoutEvent{
				RequestID:   o.RequestID,
				EscrowID:    o.EscrowID,
				Participant: attempt.Participant,
				Model:       o.Model,
				Nonce:       attempt.Nonce,
				Kind:        timeoutKind(attempt),
				Action:      TimeoutActionSkipped,
			},
		}
		if reason, skip := o.timeoutSkipReason(attempt); skip {
			step.Event.Reason = reason
		} else {
			step.Post = true
			if attempt.claimsMiss() {
				step.Kind, step.Proof = SettleByMissClaim, attempt.MissProof
			}
			step.Event.Action = TimeoutActionStarted
			step.Event.Reason = TimeoutReasonNone
		}
		steps = append(steps, step)
	}
	return steps
}

// settleSteps reports a posted vote's started event before the post and its result after, and returns the refused votes worth a retry. See README, "Timeout votes".
func settleSteps(ctx context.Context, poster TimeoutPoster, steps []TimeoutStep, escrowMissing bool, report func(TimeoutEvent)) []TimeoutStep {
	var retries []TimeoutStep
	for _, step := range steps {
		// A started event for a vote nobody attempts reads as a hung settle when no completion follows.
		if !step.Post || poster == nil {
			skipped := step.Event
			skipped.Action = TimeoutActionSkipped
			if skipped.Reason == TimeoutReasonNone {
				skipped.Reason = TimeoutReasonNoPoster
			}
			report(skipped)
			continue
		}
		if postTimeout(ctx, poster, step, escrowMissing, report) {
			retries = append(retries, step)
		}
	}
	return retries
}

var postAtOnce time.Time

type voteGroup struct {
	deadline time.Time
	steps    []TimeoutStep
}

func voteGroups(plan []TimeoutStep, poster TimeoutPoster) []voteGroup {
	var groups []voteGroup
	for _, step := range plan {
		due := voteDue(step, poster)
		index := slices.IndexFunc(groups, func(group voteGroup) bool { return group.deadline.Equal(due) })
		if index < 0 {
			groups = append(groups, voteGroup{deadline: due})
			index = len(groups) - 1
		}
		groups[index].steps = append(groups[index].steps, step)
	}
	return groups
}

func earliestDue(steps []TimeoutStep, poster TimeoutPoster) time.Time {
	earliest := voteDue(steps[0], poster)
	for _, step := range steps[1:] {
		if due := voteDue(step, poster); due.Before(earliest) {
			earliest = due
		}
	}
	return earliest
}

func voteDue(step TimeoutStep, poster TimeoutPoster) time.Time {
	if !step.Post || poster == nil {
		return postAtOnce
	}
	return poster.VoteDeadline(step.Nonce, step.StartedAt)
}

func afterAll(count int, done func()) func() {
	var remaining atomic.Int64
	remaining.Store(int64(count))
	return func() {
		if remaining.Add(-1) == 0 {
			done()
		}
	}
}

func postTimeouts(ctx context.Context, poster TimeoutPoster, steps []TimeoutStep, escrowMissing bool, report func(TimeoutEvent)) []TimeoutStep {
	var retries []TimeoutStep
	for _, step := range steps {
		if postTimeout(ctx, poster, step, escrowMissing, report) {
			retries = append(retries, step)
		}
	}
	return retries
}

func postTimeout(ctx context.Context, poster TimeoutPoster, step TimeoutStep, escrowMissing bool, report func(TimeoutEvent)) bool {
	report(step.Event)
	vote, err := poster.SettleTimeout(ctx, step)
	posted := step.Event
	posted.Kind = timeoutVoteKind(vote.Kind, posted.Kind)
	posted.Action, posted.Reason = TimeoutOutcome(vote, err, escrowMissing)
	posted.VerifyRejects, posted.Completeness = vote.VerifyRejects, vote.Completeness
	report(posted)
	return step.Event.Kind == TimeoutKindRefused && posted.Kind == TimeoutKindRefused &&
		posted.Action == TimeoutActionFailed && !escrowMissing && (vote.Unjudged || !errors.Is(err, user.ErrTimeoutNotApplied))
}

// TimeoutOutcome classifies what a posted vote came back as, preferring the handler's own detail. See README, "Timeout votes".
func TimeoutOutcome(vote TimeoutVote, err error, escrowMissing bool) (action, reason string) {
	switch {
	case errors.Is(err, user.ErrInferenceMissed):
		return TimeoutActionCompleted, TimeoutReasonNone
	case errors.Is(err, user.ErrNonceFinishedWhileWaiting):
		return TimeoutActionSkipped, TimeoutReasonNonceFinished
	case errors.Is(err, user.ErrTimeoutNotApplied):
		return TimeoutActionFailed, firstNamed(vote.Detail, TimeoutReasonNotApplied)
	case err != nil && escrowMissing:
		return TimeoutActionFailed, TimeoutReasonEscrowGone
	case err != nil:
		return TimeoutActionFailed, firstNamed(vote.Detail, TimeoutReasonCollectionError)
	}
	return TimeoutActionCompleted, TimeoutReasonNone
}

func firstNamed(detail, fallback string) string {
	if detail != "" {
		return detail
	}
	return fallback
}

// settleContext carries the race's request id into the shared session's timeout stages; an empty id adds none. See README, "Timeout votes".
func settleContext(requestID string) context.Context {
	if requestID == "" {
		return context.Background()
	}
	withRequest, _ := logging.WithRequestID(context.Background(), requestID)
	return withRequest
}
