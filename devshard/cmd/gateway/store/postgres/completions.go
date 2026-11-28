package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"devshard/cmd/gateway/store/internal/rows"
	"devshard/user"
)

// SaveInferenceCompletion records a started nonce before it is sent. Saving the same payload again is a no-op; a different one at the same nonce is user.ErrInferenceCompletionConflict. See ../README.md, "The completion journal".
func (s *Backend) SaveInferenceCompletion(escrowID string, entry user.InferenceCompletion) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encoding completion %s/%d: %w", escrowID, entry.Nonce, err)
	}
	ctx, cancel := s.database.bound(context.Background())
	defer cancel()
	return s.WithRetry(ctx, func() error {
		result, err := s.database.exec(ctx, `INSERT INTO inference_completions (escrow_id, nonce, entry_json)
			VALUES ($1, $2, $3) ON CONFLICT (escrow_id, nonce) DO NOTHING`, escrowID, entry.Nonce, data)
		if err != nil {
			return fmt.Errorf("saving completion %s/%d: %w", escrowID, entry.Nonce, err)
		}
		if inserted, err := rows.MatchedOneRow(result, escrowID); err != nil || inserted {
			return err
		}
		var stored []byte
		if err := s.database.scanRow(ctx, `SELECT entry_json FROM inference_completions WHERE escrow_id = $1 AND nonce = $2`, []any{escrowID, entry.Nonce}, &stored); err != nil {
			return fmt.Errorf("reading completion %s/%d back: %w", escrowID, entry.Nonce, err)
		}
		return rows.CheckCompletionConflict(escrowID, entry, stored)
	})
}

// HasInferenceCompletion reports whether the escrow has an entry at the nonce.
func (s *Backend) HasInferenceCompletion(escrowID string, nonce uint64) (bool, error) {
	ctx, cancel := s.database.bound(context.Background())
	defer cancel()
	var present bool
	err := s.WithRetry(ctx, func() error {
		err := s.database.scanRow(ctx, `SELECT EXISTS (SELECT 1 FROM inference_completions WHERE escrow_id = $1 AND nonce = $2)`, []any{escrowID, nonce}, &present)
		if err != nil {
			return fmt.Errorf("looking up completion %s/%d: %w", escrowID, nonce, err)
		}
		return nil
	})
	return present, err
}

// ListInferenceCompletions returns the escrow's entries ordered by nonce.
func (s *Backend) ListInferenceCompletions(escrowID string) ([]user.InferenceCompletion, error) {
	ctx, cancel := s.database.bound(context.Background())
	defer cancel()
	var entries []user.InferenceCompletion
	err := s.WithRetry(ctx, func() error {
		entries = nil
		return s.database.forEachRow(ctx, `SELECT nonce, entry_json FROM inference_completions WHERE escrow_id = $1 ORDER BY nonce`, []any{escrowID}, func(result *sql.Rows) error {
			var nonce uint64
			var data []byte
			if err := result.Scan(&nonce, &data); err != nil {
				return fmt.Errorf("scanning completion row: %w", err)
			}
			entry, err := rows.DecodeCompletion(escrowID, nonce, data)
			if err != nil {
				return err
			}
			entries = append(entries, entry)
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("listing completions of %s: %w", escrowID, err)
	}
	return entries, nil
}

// DeleteInferenceCompletion removes a finished or timed-out entry; an absent one is a no-op.
func (s *Backend) DeleteInferenceCompletion(escrowID string, nonce uint64) error {
	ctx, cancel := s.database.bound(context.Background())
	defer cancel()
	return s.WithRetry(ctx, func() error {
		if _, err := s.database.exec(ctx, `DELETE FROM inference_completions WHERE escrow_id = $1 AND nonce = $2`, escrowID, nonce); err != nil {
			return fmt.Errorf("deleting completion %s/%d: %w", escrowID, nonce, err)
		}
		return nil
	})
}
