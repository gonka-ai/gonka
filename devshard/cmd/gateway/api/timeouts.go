package api

import (
	"context"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/registry"
	"devshard/host"
	"devshard/types"
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
	return escrowVotes{session: session, prompt: dispatched.Prompt}, true
}

type escrowVotes struct {
	session registry.EscrowSession
	prompt  []byte
}

var _ engine.TimeoutPoster = escrowVotes{}

func (v escrowVotes) SettleTimeout(ctx context.Context, step engine.TimeoutStep) (engine.TimeoutVote, error) {
	return engine.NewSessionTimeouts(v.session.UserSession(), v.payload(step.Nonce)).SettleTimeout(ctx, step)
}

func (v escrowVotes) payload(nonce uint64) *host.InferencePayload {
	record, committed := v.session.Inference(nonce)
	if !committed {
		return nil
	}
	return timeoutPayload(record, v.prompt)
}

// VoteDeadline reads the escrow's own deadline for this nonce, which is what the settle queue holds it by.
func (v escrowVotes) VoteDeadline(nonce uint64, startedAt time.Time) time.Time {
	return engine.NewSessionTimeouts(v.session.UserSession(), nil).VoteDeadline(nonce, startedAt)
}

// timeoutPayload rebuilds what the host was asked for, reading every field but the prompt back from the record.
func timeoutPayload(record types.InferenceRecord, prompt []byte) *host.InferencePayload {
	return &host.InferencePayload{
		Prompt:      prompt,
		Model:       record.Model,
		InputLength: record.InputLength,
		MaxTokens:   record.MaxTokens,
		StartedAt:   record.StartedAt,
	}
}
