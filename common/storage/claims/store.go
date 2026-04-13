package claims

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS validation_claims (
    escrow_id        TEXT        NOT NULL,
    inference_id     TEXT        NOT NULL,
    epoch_id         BIGINT      NOT NULL,
    instance_address TEXT        NOT NULL,
    claimed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    tx_hash          TEXT,
    PRIMARY KEY (escrow_id, inference_id)
);
`

// Store holds a shared connection pool for the validation_claims table.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a Store and ensures the validation_claims table exists.
func New(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return nil, fmt.Errorf("claims: ensure schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Claim inserts a new validation claim. Returns true if this instance won it,
// false if another instance already claimed (escrow_id, inference_id).
func (s *Store) Claim(ctx context.Context, escrowId, inferenceId string, epochId uint64, instanceAddr string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO validation_claims (escrow_id, inference_id, epoch_id, instance_address)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (escrow_id, inference_id) DO NOTHING`,
		escrowId, inferenceId, epochId, instanceAddr,
	)
	if err != nil {
		return false, fmt.Errorf("claims: claim %s/%s: %w", escrowId, inferenceId, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReclaimOneStale atomically reassigns one stale claim for the given escrow to this instance.
// A claim is stale when tx_hash IS NULL and claimed_at < now() - ttl.
// Returns the inference_id reclaimed, or "" if none.
// FOR UPDATE SKIP LOCKED ensures concurrent instances pick different rows.
func (s *Store) ReclaimOneStale(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (string, error) {
	var inferenceId string
	err := s.pool.QueryRow(ctx,
		`WITH candidate AS (
		     SELECT escrow_id, inference_id
		     FROM validation_claims
		     WHERE escrow_id = $2
		       AND tx_hash IS NULL
		       AND claimed_at < now() - make_interval(secs => $3)
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 UPDATE validation_claims v
		 SET instance_address = $1, claimed_at = now()
		 FROM candidate
		 WHERE v.escrow_id = candidate.escrow_id
		   AND v.inference_id = candidate.inference_id
		 RETURNING v.inference_id`,
		instanceAddr, escrowId, ttl.Seconds(),
	).Scan(&inferenceId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("claims: reclaim stale: %w", err)
	}
	return inferenceId, nil
}

// SetTxHash records the submitted tx hash, marking the claim permanently complete.
func (s *Store) SetTxHash(ctx context.Context, escrowId, inferenceId, txHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE validation_claims SET tx_hash = $1
		 WHERE escrow_id = $2 AND inference_id = $3`,
		txHash, escrowId, inferenceId,
	)
	if err != nil {
		return fmt.Errorf("claims: set tx hash %s/%s: %w", escrowId, inferenceId, err)
	}
	return nil
}

// DeleteByEpoch removes all claims where epoch_id < beforeEpochId. Idempotent.
func (s *Store) DeleteByEpoch(ctx context.Context, beforeEpochId uint64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM validation_claims WHERE epoch_id < $1`,
		beforeEpochId,
	)
	if err != nil {
		return fmt.Errorf("claims: delete before epoch %d: %w", beforeEpochId, err)
	}
	return nil
}

