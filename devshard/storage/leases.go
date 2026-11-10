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

// LeaseInfo is a validation lease row as read back after an Acquire was
// refused. It exists to explain the refusal, which Acquire itself cannot do:
// ON CONFLICT DO NOTHING reports only that some row was in the way.
//
// Diagnostic only. The read is a separate statement from the failed insert, so
// the row may already have changed. Never gate behaviour on it.
//
// InstanceAddr is the participant signer address, which every devshardd
// instance of one participant shares. InstanceID identifies the process that
// wrote the row. Hostname is the container hostname, recorded for diagnosis
// and never used to decide ownership.
type LeaseInfo struct {
	InstanceAddr string
	InstanceID   string
	Hostname     string
	Status       LeaseStatus
	ClaimedAt    time.Time
}

// LeaseOwner identifies a lease holder. Address is the participant signer
// address, shared by every instance of one participant. InstanceID is unique
// to one process lifetime. Hostname is the container hostname, shared by every
// child in that container and kept across a restart; it is stored and logged,
// never matched. Only Address plus InstanceID identifies a holder. An empty
// InstanceID matches nothing, so a row written before this identity existed
// is not owned by any current process.
type LeaseOwner struct {
	Address    string
	InstanceID string
	Hostname   string
}

func (o LeaseOwner) owns(addr, instanceID string) bool {
	return o.InstanceID != "" && o.Address == addr && o.InstanceID == instanceID
}

// LeaseStore deduplicates validation work across devshardd instances.
type LeaseStore interface {
	Acquire(ctx context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) (bool, error)
	// DescribeLease reads the lease row for (epochID, escrowID, inferenceID).
	// found is false when no row exists. Diagnostic only: see LeaseInfo.
	DescribeLease(ctx context.Context, escrowID string, inferenceID, epochID uint64) (LeaseInfo, bool, error)
	// AcquireOneStale claims a pending or submitted lease whose claimed_at is
	// older than ttl. Submitted leases are retryable because the corresponding
	// validation tx may have lived only in a volatile mempool.
	AcquireOneStale(ctx context.Context, escrowID string, owner LeaseOwner, ttl time.Duration) (uint64, uint64, error)
	// SetResult updates status only when owner still holds a pending lease
	// in epochID. epochID is part of the primary key and the partition key.
	// Ownership is Address plus InstanceID. Hostname is ignored.
	SetResult(ctx context.Context, escrowID string, inferenceID, epochID uint64, status LeaseStatus, owner LeaseOwner) error
	// OwnsPendingLease reports whether owner currently holds the pending
	// lease for (epochID, escrowID, inferenceID).
	OwnsPendingLease(ctx context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) (bool, error)
	// Release deletes a pending lease this instance owns, restoring the
	// pre-acquire state so the inference can be re-picked immediately.
	// Releasing a lease that is not owned, not pending, or absent is a no-op.
	Release(ctx context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) error
}

type memoryLease struct {
	instanceAddr string
	instanceID   string
	hostname     string
	claimedAt    time.Time
	status       LeaseStatus
}

type memoryLeaseKey struct {
	epochID     uint64
	inferenceID uint64
}

func (m *Memory) Acquire(_ context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.validationLeases == nil {
		m.validationLeases = make(map[string]map[memoryLeaseKey]memoryLease)
	}
	byInference := m.validationLeases[escrowID]
	if byInference == nil {
		byInference = make(map[memoryLeaseKey]memoryLease)
		m.validationLeases[escrowID] = byInference
	}
	key := memoryLeaseKey{epochID: epochID, inferenceID: inferenceID}
	if _, exists := byInference[key]; exists {
		return false, nil
	}
	byInference[key] = memoryLease{
		instanceAddr: owner.Address,
		instanceID:   owner.InstanceID,
		hostname:     owner.Hostname,
		claimedAt:    time.Now(),
		status:       LeaseStatusPending,
	}
	return true, nil
}

func (m *Memory) DescribeLease(_ context.Context, escrowID string, inferenceID, epochID uint64) (LeaseInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.validationLeases[escrowID][memoryLeaseKey{epochID: epochID, inferenceID: inferenceID}]
	if !ok {
		return LeaseInfo{}, false, nil
	}
	return LeaseInfo{
		InstanceAddr: lease.instanceAddr,
		InstanceID:   lease.instanceID,
		Hostname:     lease.hostname,
		Status:       lease.status,
		ClaimedAt:    lease.claimedAt,
	}, true, nil
}

func (m *Memory) AcquireOneStale(_ context.Context, escrowID string, owner LeaseOwner, ttl time.Duration) (uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byInference := m.validationLeases[escrowID]
	if len(byInference) == 0 {
		return 0, 0, nil
	}
	cutoff := time.Now().Add(-ttl)
	var (
		foundKey memoryLeaseKey
		found    bool
	)
	for key, lease := range byInference {
		if (lease.status != LeaseStatusPending && lease.status != LeaseStatusSubmitted) || !lease.claimedAt.Before(cutoff) {
			continue
		}
		foundKey = key
		found = true
		break
	}
	if !found {
		return 0, 0, nil
	}
	lease := byInference[foundKey]
	lease.instanceAddr = owner.Address
	lease.instanceID = owner.InstanceID
	lease.hostname = owner.Hostname
	lease.claimedAt = time.Now()
	lease.status = LeaseStatusPending
	byInference[foundKey] = lease
	return foundKey.inferenceID, foundKey.epochID, nil
}

func (m *Memory) SetResult(_ context.Context, escrowID string, inferenceID, epochID uint64, status LeaseStatus, owner LeaseOwner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	byInference := m.validationLeases[escrowID]
	if byInference == nil {
		return ErrLeaseNotOwned
	}
	key := memoryLeaseKey{epochID: epochID, inferenceID: inferenceID}
	lease, ok := byInference[key]
	if !ok || lease.status != LeaseStatusPending || !owner.owns(lease.instanceAddr, lease.instanceID) {
		return ErrLeaseNotOwned
	}
	lease.status = status
	lease.claimedAt = time.Now()
	byInference[key] = lease
	return nil
}

func (m *Memory) OwnsPendingLease(_ context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byInference := m.validationLeases[escrowID]
	if byInference == nil {
		return false, nil
	}
	key := memoryLeaseKey{epochID: epochID, inferenceID: inferenceID}
	lease, ok := byInference[key]
	if !ok {
		return false, nil
	}
	return lease.status == LeaseStatusPending && owner.owns(lease.instanceAddr, lease.instanceID), nil
}

func (m *Memory) Release(_ context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	byInference := m.validationLeases[escrowID]
	if byInference == nil {
		return nil
	}
	key := memoryLeaseKey{epochID: epochID, inferenceID: inferenceID}
	lease, ok := byInference[key]
	if !ok || lease.status != LeaseStatusPending || !owner.owns(lease.instanceAddr, lease.instanceID) {
		return nil
	}
	delete(byInference, key)
	if len(byInference) == 0 {
		delete(m.validationLeases, escrowID)
	}
	return nil
}

func (m *Memory) pruneValidationLeasesBefore(cutoff uint64) {
	for escrowID, byInference := range m.validationLeases {
		for key := range byInference {
			if key.epochID < cutoff {
				delete(byInference, key)
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
// / SetResult / Release are no-ops: the SQLite retry loop has no shared lease
// table to reclaim from. See docs/rolling-update.md ("multi-instance ⇒ Postgres").

func (s *SQLite) Acquire(_ context.Context, _ string, _, _ uint64, _ LeaseOwner) (bool, error) {
	return true, nil
}

// DescribeLease has nothing to describe: Acquire always grants, so no caller
// ever reaches the conflict-diagnosis path on SQLite.
func (s *SQLite) DescribeLease(_ context.Context, _ string, _, _ uint64) (LeaseInfo, bool, error) {
	return LeaseInfo{}, false, nil
}

func (s *SQLite) AcquireOneStale(_ context.Context, _ string, _ LeaseOwner, _ time.Duration) (uint64, uint64, error) {
	return 0, 0, nil
}

func (s *SQLite) SetResult(_ context.Context, _ string, _, _ uint64, _ LeaseStatus, _ LeaseOwner) error {
	return nil
}

func (s *SQLite) OwnsPendingLease(_ context.Context, _ string, _, _ uint64, _ LeaseOwner) (bool, error) {
	return true, nil
}

func (s *SQLite) Release(_ context.Context, _ string, _, _ uint64, _ LeaseOwner) error {
	return nil
}

func (s *Postgres) Acquire(ctx context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) (bool, error) {
	if err := s.WaitReady(ctx); err != nil {
		return false, err
	}
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO devshard_validation_leases
		    (epoch_id, escrow_id, inference_id, instance_address, instance_id, hostname)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (epoch_id, escrow_id, inference_id) DO NOTHING`,
		epochID, escrowID, inferenceID, owner.Address, owner.InstanceID, owner.Hostname,
	)
	if err != nil {
		return false, fmt.Errorf("validation leases: acquire %s/%d: %w", escrowID, inferenceID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// DescribeLease runs only after Acquire was refused, so it deliberately trades
// atomicity for a fresh snapshot: reading in the same statement as the failed
// insert would miss a row committed by a concurrent inserter after the
// statement began, which is exactly the conflict worth reporting.
func (s *Postgres) DescribeLease(ctx context.Context, escrowID string, inferenceID, epochID uint64) (LeaseInfo, bool, error) {
	if err := s.WaitReady(ctx); err != nil {
		return LeaseInfo{}, false, err
	}
	var info LeaseInfo
	err := s.pool.QueryRow(ctx,
		`SELECT instance_address, instance_id, hostname, status, claimed_at
		 FROM devshard_validation_leases
		 WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`,
		epochID, escrowID, inferenceID,
	).Scan(&info.InstanceAddr, &info.InstanceID, &info.Hostname, &info.Status, &info.ClaimedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LeaseInfo{}, false, nil
		}
		return LeaseInfo{}, false, fmt.Errorf("validation leases: describe %s/%d: %w", escrowID, inferenceID, err)
	}
	return info, true, nil
}

func (s *Postgres) AcquireOneStale(ctx context.Context, escrowID string, owner LeaseOwner, ttl time.Duration) (uint64, uint64, error) {
	var inferenceID, epochID uint64
	err := s.pool.QueryRow(ctx,
		`WITH candidate AS (
		     SELECT epoch_id, escrow_id, inference_id
		     FROM devshard_validation_leases
		     WHERE escrow_id = $4
		       AND status IN ('pending', 'submitted')
		       AND claimed_at < now() - make_interval(secs => $5)
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )
		 UPDATE devshard_validation_leases v
		 SET instance_address = $1, instance_id = $2, hostname = $3,
		     claimed_at = now(), status = 'pending'
		 FROM candidate
		 WHERE v.epoch_id = candidate.epoch_id
		   AND v.escrow_id = candidate.escrow_id
		   AND v.inference_id = candidate.inference_id
		 RETURNING v.inference_id, v.epoch_id`,
		owner.Address, owner.InstanceID, owner.Hostname, escrowID, ttl.Seconds(),
	).Scan(&inferenceID, &epochID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("validation leases: acquire stale: %w", err)
	}
	return inferenceID, epochID, nil
}

func (s *Postgres) SetResult(ctx context.Context, escrowID string, inferenceID, epochID uint64, status LeaseStatus, owner LeaseOwner) error {
	if err := s.WaitReady(ctx); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE devshard_validation_leases SET status = $1, claimed_at = now()
		 WHERE epoch_id = $2 AND escrow_id = $3 AND inference_id = $4
		   AND instance_address = $5 AND instance_id = $6 AND instance_id <> ''
		   AND status = 'pending'`,
		status, epochID, escrowID, inferenceID, owner.Address, owner.InstanceID,
	)
	if err != nil {
		return fmt.Errorf("validation leases: set result %s/%d: %w", escrowID, inferenceID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseNotOwned
	}
	return nil
}

func (s *Postgres) Release(ctx context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) error {
	if err := s.WaitReady(ctx); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM devshard_validation_leases
		 WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3
		   AND instance_address = $4 AND instance_id = $5 AND instance_id <> ''
		   AND status = 'pending'`,
		epochID, escrowID, inferenceID, owner.Address, owner.InstanceID,
	)
	if err != nil {
		return fmt.Errorf("validation leases: release %s/%d: %w", escrowID, inferenceID, err)
	}
	return nil
}

func (s *Postgres) OwnsPendingLease(ctx context.Context, escrowID string, inferenceID, epochID uint64, owner LeaseOwner) (bool, error) {
	if err := s.WaitReady(ctx); err != nil {
		return false, err
	}
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM devshard_validation_leases
		 WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3
		   AND instance_address = $4 AND instance_id = $5 AND instance_id <> ''
		   AND status = 'pending'`,
		epochID, escrowID, inferenceID, owner.Address, owner.InstanceID,
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
