package api

import (
	"context"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/store"
	"devshard/user"
)

// OwedVotes keeps the engine's owed refusal votes in the store with the prompt each one carries, the one field the session cannot rebuild.
type OwedVotes struct{ book *store.OwedVotes }

func NewOwedVotes(book *store.OwedVotes) *OwedVotes { return &OwedVotes{book: book} }

func (v *OwedVotes) Owe(vote engine.OwedVote) {
	dispatched, isInference := vote.Params.(user.InferenceParams)
	if !isInference {
		return
	}
	v.book.Owe(store.OwedVote{
		EscrowID: vote.EscrowID, Nonce: vote.Nonce, StartedAt: vote.StartedAt,
		RequestID: vote.RequestID, Participant: vote.Participant, Model: vote.Model, Prompt: dispatched.Prompt,
	})
}

func (v *OwedVotes) Paid(escrowID string, nonce uint64) { v.book.Paid(escrowID, nonce) }

// Load returns the votes a previous process kept, with the params Sessions.Poster resolves a poster from.
func (v *OwedVotes) Load(ctx context.Context) ([]engine.OwedVote, error) {
	rows, err := v.book.Load(ctx)
	if err != nil {
		return nil, err
	}
	votes := make([]engine.OwedVote, 0, len(rows))
	for _, row := range rows {
		votes = append(votes, engine.OwedVote{
			EscrowID: row.EscrowID, Nonce: row.Nonce, StartedAt: row.StartedAt,
			RequestID: row.RequestID, Participant: row.Participant, Model: row.Model,
			Params: user.InferenceParams{Prompt: row.Prompt},
		})
	}
	return votes, nil
}
