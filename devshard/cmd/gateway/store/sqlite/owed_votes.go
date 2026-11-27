package sqlite

import (
	"context"
	"fmt"

	"devshard/cmd/gateway/store"
)

// SaveOwedVote stores the vote's row; a repeated (escrow, nonce) is a no-op.
func (s *Backend) SaveOwedVote(ctx context.Context, vote store.OwedVote) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO owed_refusal_votes (escrow_id, nonce, started_at, request_id, participant, model, prompt)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(escrow_id, nonce) DO NOTHING`,
		vote.EscrowID, vote.Nonce, store.FormatTime(vote.StartedAt), vote.RequestID, vote.Participant, vote.Model, vote.Prompt)
	return err
}

// DeleteOwedVote removes the vote's row; an absent one is a no-op.
func (s *Backend) DeleteOwedVote(ctx context.Context, escrowID string, nonce uint64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM owed_refusal_votes WHERE escrow_id = ? AND nonce = ?`, escrowID, nonce)
	return err
}

// LoadOwedVotes returns every stored vote ordered by (escrow_id, nonce).
func (s *Backend) LoadOwedVotes(ctx context.Context) ([]store.OwedVote, error) {
	result, err := s.db.QueryContext(ctx, `
		SELECT escrow_id, nonce, started_at, request_id, participant, model, prompt
		FROM owed_refusal_votes ORDER BY escrow_id, nonce`)
	if err != nil {
		return nil, fmt.Errorf("loading owed votes: %w", err)
	}
	defer result.Close()
	var votes []store.OwedVote
	for result.Next() {
		var vote store.OwedVote
		var startedAt string
		if err := result.Scan(&vote.EscrowID, &vote.Nonce, &startedAt, &vote.RequestID, &vote.Participant, &vote.Model, &vote.Prompt); err != nil {
			return nil, fmt.Errorf("scanning owed vote row: %w", err)
		}
		if vote.StartedAt, err = store.ParseTime(startedAt); err != nil {
			return nil, fmt.Errorf("parsing owed vote started_at %q: %w", startedAt, err)
		}
		votes = append(votes, vote)
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("iterating owed votes: %w", err)
	}
	return votes, nil
}
