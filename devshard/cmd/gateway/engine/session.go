package engine

import (
	"context"
	"time"

	"devshard/cmd/gateway/quorum"
	"devshard/gatewayprotocol/timeoutclaim"
	"devshard/host"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

const (
	pacedCanceledOutcome = "skipped"
	pacedCanceledDetail  = "context_canceled"

	missClaimSkippedOutcome = "skipped"
	missClaimSkippedDetail  = "unbilled_client_fault"
)

// timeoutHandler is the session's protocol-timeout entry point; *user.Session satisfies it.
type timeoutHandler interface {
	timeoutclaim.FinishSource
	timeoutclaim.SiblingSource
	HandleErrorMissWithSibling(ctx context.Context, nonce uint64, finishTx, responsePayload []byte, siblingInferenceID uint64) (user.TimeoutResult, error)
	TimeoutDeadline(nonce uint64, sendTime time.Time) (string, time.Time)
	WaitTimeoutDeadline(ctx context.Context, nonce uint64, sendTime time.Time) (reason types.TimeoutReason, ready bool, skipped user.TimeoutResult, err error)
	VoteTimeout(ctx context.Context, nonce uint64, sendTime time.Time, payload *host.InferencePayload, reason types.TimeoutReason) (result user.TimeoutResult, needsWait bool, err error)
}

// SessionTimeouts posts the chain vote that settles one escrow's unfinished nonces.
type SessionTimeouts struct {
	handler timeoutHandler
	payload *host.InferencePayload
	machine *state.StateMachine
}

func NewSessionTimeouts(session *user.Session, payload *host.InferencePayload) *SessionTimeouts {
	return &SessionTimeouts{handler: session, payload: payload, machine: session.StateMachine()}
}

// Voteable is false for a nonce no round of votes can apply. See ../quorum/README.md.
func (s *SessionTimeouts) Voteable(nonce uint64) bool {
	if s.machine == nil {
		return true
	}
	return quorum.Voteable(s.machine.SnapshotStateNoInferences().Group, s.machine.VoteThreshold(), nonce)
}

// VoteDeadline is the moment the handler's own wait would end, read before the wait rather than inside it.
func (s *SessionTimeouts) VoteDeadline(nonce uint64, startedAt time.Time) time.Time {
	_, deadline := s.handler.TimeoutDeadline(nonce, startedAt)
	return deadline
}

// SettleTimeout reads the handler's own record of whether the vote landed: the error alone cannot tell. A vote whose deadline is still ahead comes back deferred instead of waited out. See README, "Timeout votes".
func (s *SessionTimeouts) SettleTimeout(ctx context.Context, step TimeoutStep) (TimeoutVote, error) {
	result, deferred, err := s.settle(ctx, step)
	if deferred {
		return TimeoutVote{Deferred: true}, nil
	}
	vote := TimeoutVote{
		Kind:          TimeoutKind(result.Reason),
		Detail:        result.DetailReason,
		Unjudged:      result.Outcome == user.TimeoutOutcomeVoteCollectionFailed,
		VerifyRejects: result.VerifyRejects,
		Completeness:  step.Proof.completeness(),
	}
	if result.Applied {
		return vote, nil
	}
	return vote, err
}

func (s *SessionTimeouts) settle(ctx context.Context, step TimeoutStep) (result user.TimeoutResult, deferred bool, err error) {
	if step.Kind == SettleByMissClaim && step.Proof != nil {
		if finishTx := timeoutclaim.FinishTx(step.Proof.finishTx, step.Nonce, s.handler); timeoutclaim.ClaimsMiss(finishTx) {
			sibling, claimable := timeoutclaim.ClaimSibling(step.Nonce, finishTx, step.Proof.ResponsePayload, step.Siblings, s.handler)
			if !claimable {
				return user.TimeoutResult{Outcome: missClaimSkippedOutcome, DetailReason: missClaimSkippedDetail}, false, nil
			}
			result, err = s.handler.HandleErrorMissWithSibling(ctx, step.Nonce, finishTx, step.Proof.ResponsePayload, sibling)
			return result, false, err
		}
	}
	if ctx.Err() == nil && s.VoteDeadline(step.Nonce, step.StartedAt).After(time.Now()) {
		return user.TimeoutResult{}, true, nil
	}
	reason, ready, skipped, err := s.handler.WaitTimeoutDeadline(ctx, step.Nonce, step.StartedAt)
	if !ready {
		return skipped, false, err
	}
	if step.Pace != nil {
		if err := step.Pace(ctx); err != nil {
			return user.TimeoutResult{Outcome: pacedCanceledOutcome, DetailReason: pacedCanceledDetail}, false, err
		}
	}
	result, needsWait, err := s.handler.VoteTimeout(ctx, step.Nonce, step.StartedAt, s.payload, reason)
	return result, needsWait, err
}
