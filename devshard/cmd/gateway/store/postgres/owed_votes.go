package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"devshard/cmd/gateway/store"
)

// SaveOwedVote stores the vote's row; a repeated (escrow, nonce) is a no-op.
func (s *Backend) SaveOwedVote(ctx context.Context, vote store.OwedVote) error {
	_, err := s.database.exec(ctx, `
		INSERT INTO owed_refusal_votes (escrow_id, nonce, started_at, request_id, participant, model, prompt)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (escrow_id, nonce) DO NOTHING`,
		vote.EscrowID, vote.Nonce, store.FormatTime(vote.StartedAt), vote.RequestID, vote.Participant, vote.Model, vote.Prompt)
	return err
}

// DeleteOwedVote removes the vote's row; an absent one is a no-op.
func (s *Backend) DeleteOwedVote(ctx context.Context, escrowID string, nonce uint64) error {
	_, err := s.database.exec(ctx, `DELETE FROM owed_refusal_votes WHERE escrow_id = $1 AND nonce = $2`, escrowID, nonce)
	return err
}

// LoadOwedVotes returns every stored vote ordered by (escrow_id, nonce).
func (s *Backend) LoadOwedVotes(ctx context.Context) ([]store.OwedVote, error) {
	var votes []store.OwedVote
	err := s.database.forEachRow(ctx, `
		SELECT escrow_id, nonce, started_at, request_id, participant, model, prompt
		FROM owed_refusal_votes ORDER BY escrow_id, nonce`, nil, func(result *sql.Rows) error {
		var vote store.OwedVote
		var startedAt string
		if err := result.Scan(&vote.EscrowID, &vote.Nonce, &startedAt, &vote.RequestID, &vote.Participant, &vote.Model, &vote.Prompt); err != nil {
			return fmt.Errorf("scanning owed vote row: %w", err)
		}
		parsed, err := store.ParseTime(startedAt)
		if err != nil {
			return fmt.Errorf("parsing owed vote started_at %q: %w", startedAt, err)
		}
		vote.StartedAt = parsed
		votes = append(votes, vote)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading owed votes: %w", err)
	}
	return votes, nil
}
