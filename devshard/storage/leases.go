package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LeaseStatus is the status of a validation lease row.
type LeaseStatus string

// Lease status values.
const (
	LeaseStatusPending   LeaseStatus = "pending"
	LeaseStatusSubmitted LeaseStatus = "submitted"
	LeaseStatusSkipped   LeaseStatus = "skipped"
)

// ErrLeaseNotOwned is returned when SetResult matches no pending row for this
// instance (stolen via AcquireOneStale, already completed, or never acquired).
var ErrLeaseNotOwned = errors.New("validation lease not owned or not pending")

// LeaseStore deduplicates validation work across devshardd instances.
type LeaseStore interface {
	Acquire(ctx context.Context, escrowID string, inferenceID, epochID uint64, instanceAddr string) (bool, error)
	AcquireOneStale(ctx context.Context, escrowID, instanceAddr string, ttl time.Duration) (uint64, uint64, error)
	// SetResult updates status only when instanceAddr still owns a pending lease.
	SetResult(ctx context.Context, escrowID string, inferenceID uint64, status LeaseStatus, instanceAddr string) error
	// OwnsPendingLease reports whether instanceAddr currently holds the pending lease.
	OwnsPendingLease(ctx context.Context, escrowID string, inferenceID uint64, instanceAddr string) (bool, error)
}

type memoryLease struct {
	epochID      uint64
	instanceAddr string
	claimedAt    time.Time
	status       LeaseStatus
}

func (m *Memory) Acquire(_ context.Context, escrowID string, inferenceID, epochID uint64, instanceAddr string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.validationLeases == nil {
		m.validationLeases = make(map[string]map[uint64]memoryLease)
	}
	byInference := m.validationLeases[escrowID]
	if byInference == nil {
		byInference = make(map[uint64]memoryLease)
		m.validationLeases[escrowID] = byInference
	}
	if _, exists := byInference[inferenceID]; exists {
		return false, nil
	}
	byInference[inferenceID] = memoryLease{
		epochID:      epochID,
		instanceAddr: instanceAddr,
		claimedAt:    time.Now(),
		status:       LeaseStatusPending,
	}
	return true, nil
}

func (m *Memory) AcquireOneStale(_ context.Context, escrowID, instanceAddr string, ttl time.Duration) (uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byInference := m.validationLeases[escrowID]
	if len(byInference) == 0 {
		return 0, 0, nil
	}
	cutoff := time.Now().Add(-ttl)
	var (
		foundID    uint64
		foundEpoch uint64
		found      bool
	)
	for inferenceID, lease := range byInference {
		if lease.status != LeaseStatusPending || !lease.claimedAt.Before(cutoff) {
			continue
		}
		foundID = inferenceID
		foundEpoch = lease.epochID
		found = true
		break
	}
	if !found {
		return 0, 0, nil
	}
	lease := byInference[foundID]
	lease.instanceAddr = instanceAddr
	lease.claimedAt = time.Now()
	byInference[foundID] = lease
	return foundID, foundEpoch, nil
}

func (m *Memory) SetResult(_ context.Context, escrowID string, inferenceID uint64, status LeaseStatus, instanceAddr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	byInference := m.validationLeases[escrowID]
	if byInference == nil {
		return ErrLeaseNotOwned
	}
	lease, ok := byInference[inferenceID]
	if !ok || lease.status != LeaseStatusPending || lease.instanceAddr != instanceAddr {
		return ErrLeaseNotOwned
	}
	lease.status = status
	byInference[inferenceID] = lease
	return nil
}

func (m *Memory) OwnsPendingLease(_ context.Context, escrowID string, inferenceID uint64, instanceAddr string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byInference := m.validationLeases[escrowID]
	if byInference == nil {
		return false, nil
	}
	lease, ok := byInference[inferenceID]
	if !ok {
		return false, nil
	}
	return lease.status == LeaseStatusPending && lease.instanceAddr == instanceAddr, nil
}

func (m *Memory) pruneValidationLeasesBefore(cutoff uint64) {
	for escrowID, byInference := range m.validationLeases {
		for inferenceID, lease := range byInference {
			if lease.epochID < cutoff {
				delete(byInference, inferenceID)
			}
		}
		if len(byInference) == 0 {
			delete(m.validationLeases, escrowID)
		}
	}
}

// SQLite validation leases are intentionally no-ops.
//
// Leases exist only to deduplicate validation across multiple devshardd
// instances that share a backing store. The SQLite backend is single-instance
// by construction (single-writer file; see storage/factory.go and
// docs/storage-design.md), so there is never a second instance to coordinate
// with. Any multi-instance / HA deployment must run on Postgres, whose lease
// table provides the real cross-instance dedup.
//
// Acquire therefore always grants (validation runs inline), and AcquireOneStale
// / SetResult are no-ops: the SQLite retry loop has no shared lease table to
// reclaim from. See docs/rolling-update.md ("multi-instance ⇒ Postgres").

func (s *SQLite) Acquire(_ context.Context, _ string, _, _ uint64, _ string) (bool, error) {
	return true, nil
}

func (s *SQLite) AcquireOneStale(_ context.Context, _, _ string, _ time.Duration) (uint64, uint64, error) {
	return 0, 0, nil
}

func (s *SQLite) SetResult(_ context.Context, _ string, _ uint64, _ LeaseStatus, _ string) error {
	return nil
}

func (s *SQLite) OwnsPendingLease(_ context.Context, _ string, _ uint64, _ string) (bool, error) {
	return true, nil
}

func (s *Postgres) Acquire(ctx context.Context, escrowID string, inferenceID, epochID uint64, instanceAddr string) (bool, error) {
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO devshard_validation_leases (epoch_id, escrow_id, inference_id, instance_address)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (epoch_id, escrow_id, inference_id) DO NOTHING`,
		epochID, escrowID, inferenceID, instanceAddr,
	)
	if err != nil {
		return false, fmt.Errorf("validation leases: acquire %s/%d: %w", escrowID, inferenceID, err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Postgres) AcquireOneStale(ctx context.Context, escrowID, instanceAddr string, ttl time.Duration) (uint64, uint64, error) {
	var inferenceID, epochID uint64
	err := s.pool.QueryRow(ctx,
		`WITH candidate AS (
		     SELECT epoch_id, escrow_id, inference_id
		     FROM devshard_validation_leases
		     WHERE escrow_id = $2
		       AND status = 'pending'
		       AND claimed_at < now() - make_interval(secs => $3)
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 UPDATE devshard_validation_leases v
		 SET instance_address = $1, claimed_at = now()
		 FROM candidate
		 WHERE v.epoch_id = candidate.epoch_id
		   AND v.escrow_id = candidate.escrow_id
		   AND v.inference_id = candidate.inference_id
		 RETURNING v.inference_id, v.epoch_id`,
		instanceAddr, escrowID, ttl.Seconds(),
	).Scan(&inferenceID, &epochID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("validation leases: acquire stale: %w", err)
	}
	return inferenceID, epochID, nil
}

func (s *Postgres) SetResult(ctx context.Context, escrowID string, inferenceID uint64, status LeaseStatus, instanceAddr string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE devshard_validation_leases SET status = $1
		 WHERE escrow_id = $2 AND inference_id = $3
		   AND instance_address = $4 AND status = 'pending'`,
		status, escrowID, inferenceID, instanceAddr,
	)
	if err != nil {
		return fmt.Errorf("validation leases: set result %s/%d: %w", escrowID, inferenceID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseNotOwned
	}
	return nil
}

func (s *Postgres) OwnsPendingLease(ctx context.Context, escrowID string, inferenceID uint64, instanceAddr string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM devshard_validation_leases
		 WHERE escrow_id = $1 AND inference_id = $2
		   AND instance_address = $3 AND status = 'pending'
		 LIMIT 1`,
		escrowID, inferenceID, instanceAddr,
	).Scan(&one)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("validation leases: owns pending %s/%d: %w", escrowID, inferenceID, err)
	}
	return true, nil
}

var (
	_ LeaseStore = (*Memory)(nil)
	_ LeaseStore = (*SQLite)(nil)
	_ LeaseStore = (*Postgres)(nil)
	_ LeaseStore = (*HybridStorage)(nil)
	_ LeaseStore = (*ManagedStorage)(nil)
)
