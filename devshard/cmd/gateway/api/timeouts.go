package api

import (
	"context"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/registry"
	"devshard/gatewayprotocol/timeoutclaim"
	"devshard/host"
	"devshard/user"
)

// Poster resolves the vote poster for one escrow. A retired escrow still resolves: its nonces have no other path.
func (s Sessions) Poster(escrowID string, params any) (engine.TimeoutPoster, bool) {
	dispatched, isInference := params.(user.InferenceParams)
	if !isInference {
		return nil, false
	}
	session, held := s.escrows.SettlementSession(escrowID)
	if !held {
		return nil, false
	}
	return escrowVotes{session: session, prompt: dispatched.Prompt, startedAt: dispatched.StartedAt}, true
}

type escrowVotes struct {
	session   registry.EscrowSession
	prompt    []byte
	startedAt int64
}

var _ engine.TimeoutPoster = escrowVotes{}

func (v escrowVotes) SettleTimeout(ctx context.Context, step engine.TimeoutStep) (engine.TimeoutVote, error) {
	return engine.NewSessionTimeouts(v.session.UserSession(), v.payload(step.Nonce)).SettleTimeout(ctx, step)
}

func (v escrowVotes) payload(nonce uint64) *host.InferencePayload {
	return timeoutclaim.Payload(v.session.Inference, nonce, v.prompt, v.startedAt)
}

// VoteDeadline reads the escrow's own deadline for this nonce, which is what the settle queue holds it by.
func (v escrowVotes) VoteDeadline(nonce uint64, startedAt time.Time) time.Time {
	return engine.NewSessionTimeouts(v.session.UserSession(), nil).VoteDeadline(nonce, startedAt)
}

// Voteable reads the escrow's own group, so a nonce is judged by the slots of the escrow it was spent on.
func (v escrowVotes) Voteable(nonce uint64) bool {
	return engine.NewSessionTimeouts(v.session.UserSession(), nil).Voteable(nonce)
}
