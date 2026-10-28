package engine

import (
	"context"
	"time"

	"devshard/host"
	"devshard/user"
)

// timeoutHandler is the session's protocol-timeout entry point; *user.Session satisfies it.
type timeoutHandler interface {
	HandleTimeout(ctx context.Context, nonce uint64, sendTime time.Time, payload *host.InferencePayload) (user.TimeoutResult, error)
	HandleErrorMiss(ctx context.Context, nonce uint64, finishTx, responsePayload []byte) (user.TimeoutResult, error)
	FinishTxFor(inferenceID uint64) []byte
	TimeoutDeadline(nonce uint64, sendTime time.Time) (string, time.Time)
}

// SessionTimeouts posts the chain vote that settles one escrow's unfinished nonces.
type SessionTimeouts struct {
	handler timeoutHandler
	payload *host.InferencePayload
}

func NewSessionTimeouts(session *user.Session, payload *host.InferencePayload) *SessionTimeouts {
	return &SessionTimeouts{handler: session, payload: payload}
}

// VoteDeadline is the moment the handler's own wait would end, read before the wait rather than inside it.
func (s *SessionTimeouts) VoteDeadline(nonce uint64, startedAt time.Time) time.Time {
	_, deadline := s.handler.TimeoutDeadline(nonce, startedAt)
	return deadline
}

// SettleTimeout reads the handler's own record of whether the vote landed: the error alone cannot tell. See README, "Timeout votes".
func (s *SessionTimeouts) SettleTimeout(ctx context.Context, step TimeoutStep) (TimeoutVote, error) {
	result, err := s.settle(ctx, step)
	vote := TimeoutVote{
		Kind:          result.Reason,
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

func (s *SessionTimeouts) settle(ctx context.Context, step TimeoutStep) (user.TimeoutResult, error) {
	if step.Kind == SettleByMissClaim && step.Proof != nil {
		if finishTx := s.handler.FinishTxFor(step.Nonce); len(finishTx) > 0 {
			return s.handler.HandleErrorMiss(ctx, step.Nonce, finishTx, step.Proof.ResponsePayload)
		}
	}
	return s.handler.HandleTimeout(ctx, step.Nonce, step.StartedAt, s.payload)
}
