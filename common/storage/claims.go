package storage

import (
	"context"
	"fmt"
)

const claimsSchema = `
CREATE TABLE IF NOT EXISTS validation_claims (
    inference_id     TEXT        PRIMARY KEY,
    epoch_id         BIGINT      NOT NULL,
    instance_address TEXT        NOT NULL,
    claimed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    tx_hash          TEXT
);
`

func (s *Storage) ensureClaimsSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, claimsSchema); err != nil {
		return fmt.Errorf("storage: ensure validation_claims schema: %w", err)
	}
	return nil
}

// Claim inserts a new validation claim for this instance.
// Returns true if this instance won the claim, false if another instance already claimed it.
// Uses INSERT ... ON CONFLICT DO NOTHING as the coordination primitive.
func (s *Storage) Claim(ctx context.Context, inferenceID string, epochID uint64, instanceAddress string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO validation_claims (inference_id, epoch_id, instance_address)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (inference_id) DO NOTHING`,
		inferenceID, epochID, instanceAddress,
	)
	if err != nil {
		return false, fmt.Errorf("storage: claim %s: %w", inferenceID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReclaimOneStale atomically reassigns one stale claim to this instance.
// A claim is stale if tx_hash IS NULL and claimed_at is older than the configured TTL.
// Returns the inference_id that was reclaimed, or "" if no stale claims exist.
// FOR UPDATE SKIP LOCKED ensures concurrent instances each pick a different row.
func (s *Storage) ReclaimOneStale(ctx context.Context, instanceAddress string, ttlInterval string) (string, error) {
	var inferenceID string
	err := s.pool.QueryRow(ctx,
		`UPDATE validation_claims
		 SET instance_address = $1, claimed_at = now()
		 WHERE inference_id = (
		     SELECT inference_id FROM validation_claims
		     WHERE tx_hash IS NULL
		       AND claimed_at < now() - $2::interval
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING inference_id`,
		instanceAddress, ttlInterval,
	).Scan(&inferenceID)
	if err != nil {
		if isNoRows(err) {
			return "", nil
		}
		return "", fmt.Errorf("storage: reclaim stale: %w", err)
	}
	return inferenceID, nil
}

// SetTxHash records the submitted tx hash for a claim, marking it permanently complete.
// Once set, the claim will not be retried.
func (s *Storage) SetTxHash(ctx context.Context, inferenceID, txHash string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE validation_claims SET tx_hash = $1 WHERE inference_id = $2`,
		txHash, inferenceID,
	)
	if err != nil {
		return fmt.Errorf("storage: set tx hash %s: %w", inferenceID, err)
	}
	return nil
}

// DeleteClaimsByEpoch removes all claims for epochs older than the given epoch ID.
// Safe to run concurrently across instances — idempotent.
func (s *Storage) DeleteClaimsByEpoch(ctx context.Context, beforeEpochID uint64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM validation_claims WHERE epoch_id < $1`,
		beforeEpochID,
	)
	if err != nil {
		return fmt.Errorf("storage: delete claims before epoch %d: %w", beforeEpochID, err)
	}
	return nil
}
