package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"devshard/cmd/gateway/store"
)

// SaveCommitment returns once Postgres has committed the row; the commit is flushed to disk before the server answers, which boot checks and the session setting keeps on. See ../README.md, "The commitment row".
func (s *Backend) SaveCommitment(ctx context.Context, commitment store.Commitment) error {
	_, err := s.database.exec(ctx, `
		INSERT INTO escrow_rotation_commitments (tx_hash, model, role, private_key_env, epoch, block_height, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tx_hash) DO UPDATE SET
			model = excluded.model,
			role = excluded.role,
			private_key_env = excluded.private_key_env,
			epoch = excluded.epoch,
			block_height = excluded.block_height,
			created_at = excluded.created_at`,
		commitment.TxHash, commitment.Model, string(commitment.Role), commitment.PrivateKeyEnv, commitment.Epoch, commitment.BlockHeight,
		store.FormatTime(commitment.CreatedAt))
	if err != nil {
		return fmt.Errorf("saving commitment %s: %w", commitment.TxHash, err)
	}
	return nil
}

// LoadCommitments returns every commitment ordered by (created_at, tx_hash).
func (s *Backend) LoadCommitments(ctx context.Context) ([]store.Commitment, error) {
	var commitments []store.Commitment
	err := s.database.forEachRow(ctx, `
		SELECT tx_hash, model, role, private_key_env, epoch, block_height, created_at
		FROM escrow_rotation_commitments ORDER BY created_at, tx_hash`, nil, func(result *sql.Rows) error {
		var commitment store.Commitment
		var createdAt string
		if err := result.Scan(&commitment.TxHash, &commitment.Model, &commitment.Role, &commitment.PrivateKeyEnv,
			&commitment.Epoch, &commitment.BlockHeight, &createdAt); err != nil {
			return fmt.Errorf("scanning commitment row: %w", err)
		}
		parsedCreatedAt, err := store.ParseTime(createdAt)
		if err != nil {
			return fmt.Errorf("parsing commitment created_at %q: %w", createdAt, err)
		}
		commitment.CreatedAt = parsedCreatedAt
		commitments = append(commitments, commitment)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading commitments: %w", err)
	}
	return commitments, nil
}

// DeleteCommitment removes the commitment for txHash; an absent hash is a no-op.
func (s *Backend) DeleteCommitment(ctx context.Context, txHash string) error {
	if _, err := s.database.exec(ctx, `DELETE FROM escrow_rotation_commitments WHERE tx_hash = $1`, txHash); err != nil {
		return fmt.Errorf("deleting commitment %s: %w", txHash, err)
	}
	return nil
}
