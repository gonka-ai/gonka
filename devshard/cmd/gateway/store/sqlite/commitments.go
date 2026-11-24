package sqlite

import (
	"context"
	"fmt"

	"devshard/cmd/gateway/store"
)

// SaveCommitment returns nil only once the row is in the main database file, which a machine crash cannot take back. See ../README.md, "The commitment row".
func (s *Backend) SaveCommitment(ctx context.Context, commitment store.Commitment) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO escrow_rotation_commitments (tx_hash, model, role, private_key_env, epoch, block_height, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tx_hash) DO UPDATE SET
			model = excluded.model,
			role = excluded.role,
			private_key_env = excluded.private_key_env,
			epoch = excluded.epoch,
			block_height = excluded.block_height,
			created_at = excluded.created_at`,
		commitment.TxHash, commitment.Model, commitment.Role, commitment.PrivateKeyEnv, commitment.Epoch, commitment.BlockHeight,
		store.FormatTime(commitment.CreatedAt))
	if err != nil {
		return fmt.Errorf("saving commitment %s: %w", commitment.TxHash, err)
	}
	if err := s.checkpoint(ctx); err != nil {
		return fmt.Errorf("saving commitment %s: %w", commitment.TxHash, err)
	}
	return nil
}

func (s *Backend) checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointedFrames int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(FULL)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("checkpointing: %w", err)
	}
	if busy != checkpointNotBusy {
		return errCheckpointBusy
	}
	return nil
}

// LoadCommitments returns every commitment ordered by (created_at, tx_hash).
func (s *Backend) LoadCommitments(ctx context.Context) ([]store.Commitment, error) {
	result, err := s.db.QueryContext(ctx, `
		SELECT tx_hash, model, role, private_key_env, epoch, block_height, created_at
		FROM escrow_rotation_commitments ORDER BY created_at, tx_hash`)
	if err != nil {
		return nil, fmt.Errorf("loading commitments: %w", err)
	}
	defer result.Close()
	var commitments []store.Commitment
	for result.Next() {
		var commitment store.Commitment
		var createdAt string
		if err := result.Scan(&commitment.TxHash, &commitment.Model, &commitment.Role, &commitment.PrivateKeyEnv,
			&commitment.Epoch, &commitment.BlockHeight, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning commitment row: %w", err)
		}
		parsedCreatedAt, err := store.ParseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parsing commitment created_at %q: %w", createdAt, err)
		}
		commitment.CreatedAt = parsedCreatedAt
		commitments = append(commitments, commitment)
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("iterating commitments: %w", err)
	}
	return commitments, nil
}

// DeleteCommitment removes the commitment for txHash; an absent hash is a no-op.
func (s *Backend) DeleteCommitment(ctx context.Context, txHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM escrow_rotation_commitments WHERE tx_hash = ?`, txHash); err != nil {
		return fmt.Errorf("deleting commitment %s: %w", txHash, err)
	}
	return nil
}
