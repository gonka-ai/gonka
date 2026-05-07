package validationlease

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaseStatus is the status of a validation lease row.
type LeaseStatus string

// Lease status values.
const (
	StatusPending   LeaseStatus = "pending"
	StatusSubmitted LeaseStatus = "submitted" // validation result added to session mempool
	StatusSkipped   LeaseStatus = "skipped"   // epoch stale or other non-error skip
)

const schema = `
CREATE TABLE IF NOT EXISTS validation_leases (
    escrow_id        TEXT        NOT NULL,
    inference_id     BIGINT      NOT NULL,
    epoch_id         BIGINT      NOT NULL,
    instance_address TEXT        NOT NULL,
    claimed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    status           TEXT        NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'submitted', 'skipped')),
    PRIMARY KEY (escrow_id, inference_id)
);
`

// Store holds a shared connection pool for the validation_leases table.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a Store and ensures the validation_leases table exists.
func New(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return nil, fmt.Errorf("validation leases: ensure schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Acquire inserts a new validation lease. Returns true if this instance acquired
// the lease, false if another instance already holds (escrow_id, inference_id).
func (s *Store) Acquire(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO validation_leases (escrow_id, inference_id, epoch_id, instance_address)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (escrow_id, inference_id) DO NOTHING`,
		escrowId, inferenceId, epochId, instanceAddr,
	)
	if err != nil {
		return false, fmt.Errorf("validation leases: acquire %s/%d: %w", escrowId, inferenceId, err)
	}
	return tag.RowsAffected() == 1, nil
}

// AcquireOneStale atomically reassigns one stale lease for the given escrow to this instance.
// A lease is stale when status = 'pending' and claimed_at < now() - ttl.
// Returns the inference_id and epoch_id acquired, or (0, 0) if none.
// FOR UPDATE SKIP LOCKED ensures concurrent instances pick different rows.
func (s *Store) AcquireOneStale(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error) {
	var inferenceId, epochId uint64
	err := s.pool.QueryRow(ctx,
		`WITH candidate AS (
		     SELECT escrow_id, inference_id
		     FROM validation_leases
		     WHERE escrow_id = $2
		       AND status = 'pending'
		       AND claimed_at < now() - make_interval(secs => $3)
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 UPDATE validation_leases v
		 SET instance_address = $1, claimed_at = now()
		 FROM candidate
		 WHERE v.escrow_id = candidate.escrow_id
		   AND v.inference_id = candidate.inference_id
		 RETURNING v.inference_id, v.epoch_id`,
		instanceAddr, escrowId, ttl.Seconds(),
	).Scan(&inferenceId, &epochId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("validation leases: acquire stale: %w", err)
	}
	return inferenceId, epochId, nil
}

// SetResult marks a lease as resolved with the given status.
func (s *Store) SetResult(ctx context.Context, escrowId string, inferenceId uint64, status LeaseStatus) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE validation_leases SET status = $1
		 WHERE escrow_id = $2 AND inference_id = $3`,
		status, escrowId, inferenceId,
	)
	if err != nil {
		return fmt.Errorf("validation leases: set result %s/%d: %w", escrowId, inferenceId, err)
	}
	return nil
}

// DeleteBeforeEpoch removes all leases where epoch_id < epochId. Idempotent.
func (s *Store) DeleteBeforeEpoch(ctx context.Context, epochId uint64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM validation_leases WHERE epoch_id < $1`,
		epochId,
	)
	if err != nil {
		return fmt.Errorf("validation leases: delete before epoch %d: %w", epochId, err)
	}
	return nil
}
