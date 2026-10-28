package store

import (
	"context"
	"fmt"
	"time"
)

// Commitment is the durable intent for one escrow create, written before the tx broadcasts. See README.md, "The commitment row".
type Commitment struct {
	TxHash        string
	Model         string
	Role          string
	PrivateKeyEnv string
	Epoch         uint64
	BlockHeight   int64
	CreatedAt     time.Time
}

func (s *Store) SaveCommitment(ctx context.Context, c Commitment) error {
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
		c.TxHash, c.Model, c.Role, c.PrivateKeyEnv, c.Epoch, c.BlockHeight,
		FormatTime(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("saving commitment %s: %w", c.TxHash, err)
	}
	return nil
}

// LoadCommitments returns every commitment ordered by (created_at, tx_hash).
func (s *Store) LoadCommitments(ctx context.Context) ([]Commitment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tx_hash, model, role, private_key_env, epoch, block_height, created_at
		FROM escrow_rotation_commitments ORDER BY created_at, tx_hash`)
	if err != nil {
		return nil, fmt.Errorf("loading commitments: %w", err)
	}
	defer rows.Close()
	var commitments []Commitment
	for rows.Next() {
		var c Commitment
		var createdAt string
		if err := rows.Scan(&c.TxHash, &c.Model, &c.Role, &c.PrivateKeyEnv,
			&c.Epoch, &c.BlockHeight, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning commitment row: %w", err)
		}
		parsedCreatedAt, err := parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parsing commitment created_at %q: %w", createdAt, err)
		}
		c.CreatedAt = parsedCreatedAt
		commitments = append(commitments, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating commitments: %w", err)
	}
	return commitments, nil
}

// DeleteCommitment removes the commitment for txHash; an absent hash is a no-op.
func (s *Store) DeleteCommitment(ctx context.Context, txHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM escrow_rotation_commitments WHERE tx_hash = ?`, txHash); err != nil {
		return fmt.Errorf("deleting commitment %s: %w", txHash, err)
	}
	return nil
}
