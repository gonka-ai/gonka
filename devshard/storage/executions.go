package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExecutionStatus is the durable state of one inference execution.
type ExecutionStatus string

const (
	ExecutionClaimed    ExecutionStatus = "claimed"
	ExecutionDispatched ExecutionStatus = "dispatched"
	ExecutionCompleted  ExecutionStatus = "completed"
	ExecutionAbandoned  ExecutionStatus = "abandoned"
)

// executionClaimLease bounds the crash window before dispatch. A claimant
// that resumes after its lease was fenced out cannot send the request because
// MarkExecutionDispatched verifies the new fence first. Dispatched claims are
// never stolen: the external ML request may already have taken effect.
const executionClaimLease = 2 * time.Minute

var (
	ErrExecutionNotFound         = errors.New("execution claim not found")
	ErrExecutionClaimNotOwned    = errors.New("execution claim state, owner, or fence does not match")
	ErrExecutionOutcomeUncertain = errors.New(
		"execution was dispatched but its result was not durably recorded",
	)
)

// ExecutionClaim is returned for both a newly acquired and an existing claim.
// Result is populated only for completed executions.
type ExecutionClaim struct {
	Acquired bool
	Fence    uint64
	Status   ExecutionStatus
	Result   []byte
}

// ExecutionStore serializes the external ML side effect across devshardd
// replicas sharing a database. Only an expired pre-dispatch claim can be
// stolen; a dispatched claim is never retried because the ML POST may already
// have taken effect.
type ExecutionStore interface {
	ClaimExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string) (ExecutionClaim, error)
	GetExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64) (ExecutionClaim, error)
	MarkExecutionDispatched(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64) error
	AbandonExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64) error
	CompleteExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64, result []byte) error
}

type executionKey struct {
	epochID     uint64
	escrowID    string
	inferenceID uint64
}

type memoryExecution struct {
	ownerID string
	fence   uint64
	status  ExecutionStatus
	result  []byte
	claimed time.Time
}

func (m *Memory) ClaimExecution(_ context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string) (ExecutionClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.executionClaims == nil {
		m.executionClaims = make(map[executionKey]memoryExecution)
	}
	key := executionKey{epochID: epochID, escrowID: escrowID, inferenceID: inferenceID}
	if existing, ok := m.executionClaims[key]; ok {
		claimExpired := existing.status == ExecutionClaimed && time.Since(existing.claimed) >= executionClaimLease
		if existing.status != ExecutionAbandoned && !claimExpired {
			return memoryExecutionClaim(existing, false), nil
		}
	}
	m.nextExecutionFence++
	claim := memoryExecution{
		ownerID: ownerID,
		fence:   m.nextExecutionFence,
		status:  ExecutionClaimed,
		claimed: time.Now(),
	}
	m.executionClaims[key] = claim
	return memoryExecutionClaim(claim, true), nil
}

func (m *Memory) GetExecution(_ context.Context, epochID uint64, escrowID string, inferenceID uint64) (ExecutionClaim, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	claim, ok := m.executionClaims[executionKey{epochID: epochID, escrowID: escrowID, inferenceID: inferenceID}]
	if !ok {
		return ExecutionClaim{}, ErrExecutionNotFound
	}
	return memoryExecutionClaim(claim, false), nil
}

func (m *Memory) CompleteExecution(_ context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64, result []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := executionKey{epochID: epochID, escrowID: escrowID, inferenceID: inferenceID}
	claim, ok := m.executionClaims[key]
	if !ok || claim.status != ExecutionDispatched || claim.ownerID != ownerID || claim.fence != fence {
		return ErrExecutionClaimNotOwned
	}
	claim.status = ExecutionCompleted
	claim.result = append([]byte(nil), result...)
	m.executionClaims[key] = claim
	return nil
}

func (m *Memory) MarkExecutionDispatched(_ context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := executionKey{epochID: epochID, escrowID: escrowID, inferenceID: inferenceID}
	claim, ok := m.executionClaims[key]
	if !ok || claim.status != ExecutionClaimed || claim.ownerID != ownerID || claim.fence != fence {
		return ErrExecutionClaimNotOwned
	}
	claim.status = ExecutionDispatched
	m.executionClaims[key] = claim
	return nil
}

func (m *Memory) AbandonExecution(_ context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := executionKey{epochID: epochID, escrowID: escrowID, inferenceID: inferenceID}
	claim, ok := m.executionClaims[key]
	if !ok || claim.status != ExecutionClaimed || claim.ownerID != ownerID || claim.fence != fence {
		return ErrExecutionClaimNotOwned
	}
	claim.status = ExecutionAbandoned
	m.executionClaims[key] = claim
	return nil
}

func memoryExecutionClaim(claim memoryExecution, acquired bool) ExecutionClaim {
	return ExecutionClaim{
		Acquired: acquired,
		Fence:    claim.fence,
		Status:   claim.status,
		Result:   append([]byte(nil), claim.result...),
	}
}

func (m *Memory) pruneExecutionClaimsBefore(cutoff uint64) {
	for key := range m.executionClaims {
		if key.epochID < cutoff {
			delete(m.executionClaims, key)
		}
	}
}

// SQLite is single-instance by contract, so its execution claim is local and
// always granted. Multi-instance deployments are required to use Postgres.
func (s *SQLite) ClaimExecution(_ context.Context, _ uint64, _ string, _ uint64, _ string) (ExecutionClaim, error) {
	return ExecutionClaim{Acquired: true, Fence: 1, Status: ExecutionClaimed}, nil
}

func (s *SQLite) GetExecution(_ context.Context, _ uint64, _ string, _ uint64) (ExecutionClaim, error) {
	return ExecutionClaim{}, ErrExecutionNotFound
}

func (s *SQLite) CompleteExecution(_ context.Context, _ uint64, _ string, _ uint64, _ string, _ uint64, _ []byte) error {
	return nil
}

func (s *SQLite) MarkExecutionDispatched(_ context.Context, _ uint64, _ string, _ uint64, _ string, _ uint64) error {
	return nil
}

func (s *SQLite) AbandonExecution(_ context.Context, _ uint64, _ string, _ uint64, _ string, _ uint64) error {
	return nil
}

func (s *Postgres) ClaimExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string) (ExecutionClaim, error) {
	if err := s.WaitReady(ctx); err != nil {
		return ExecutionClaim{}, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, postgresOpTimeout)
	defer cancel()
	var fence uint64
	err := s.pool.QueryRow(queryCtx, `
INSERT INTO devshard_execution_claims (epoch_id, escrow_id, inference_id, owner_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (epoch_id, escrow_id, inference_id) DO UPDATE
SET owner_id = EXCLUDED.owner_id,
    fence = nextval('devshard_execution_fence_seq'),
    phase = 'claimed',
    result = NULL,
    claimed_at = now()
WHERE devshard_execution_claims.phase = 'abandoned'
   OR (devshard_execution_claims.phase = 'claimed'
       AND devshard_execution_claims.claimed_at < now() - $5::interval)
RETURNING fence`, epochID, escrowID, inferenceID, ownerID, executionClaimLease.String()).Scan(&fence)
	if err == nil {
		return ExecutionClaim{Acquired: true, Fence: fence, Status: ExecutionClaimed}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ExecutionClaim{}, fmt.Errorf("execution claim %s/%d: %w", escrowID, inferenceID, err)
	}
	return s.getExecution(queryCtx, epochID, escrowID, inferenceID)
}

func (s *Postgres) GetExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64) (ExecutionClaim, error) {
	queryCtx, cancel := context.WithTimeout(ctx, postgresOpTimeout)
	defer cancel()
	return s.getExecution(queryCtx, epochID, escrowID, inferenceID)
}

func (s *Postgres) getExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64) (ExecutionClaim, error) {
	var claim ExecutionClaim
	err := s.pool.QueryRow(ctx, `
SELECT fence, phase, result
FROM devshard_execution_claims
WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`, epochID, escrowID, inferenceID).
		Scan(&claim.Fence, &claim.Status, &claim.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExecutionClaim{}, ErrExecutionNotFound
	}
	if err != nil {
		return ExecutionClaim{}, fmt.Errorf("get execution claim %s/%d: %w", escrowID, inferenceID, err)
	}
	return claim, nil
}

func (s *Postgres) MarkExecutionDispatched(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64) error {
	queryCtx, cancel := context.WithTimeout(ctx, postgresOpTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `
UPDATE devshard_execution_claims
SET phase = 'dispatched'
WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3
  AND owner_id = $4 AND fence = $5 AND phase = 'claimed'`,
		epochID, escrowID, inferenceID, ownerID, fence)
	if err != nil {
		return fmt.Errorf("dispatch execution claim %s/%d: %w", escrowID, inferenceID, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionClaimNotOwned
	}
	return nil
}

func (s *Postgres) AbandonExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64) error {
	queryCtx, cancel := context.WithTimeout(ctx, postgresOpTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `
UPDATE devshard_execution_claims
SET phase = 'abandoned'
WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3
  AND owner_id = $4 AND fence = $5 AND phase = 'claimed'`,
		epochID, escrowID, inferenceID, ownerID, fence)
	if err != nil {
		return fmt.Errorf("abandon execution claim %s/%d: %w", escrowID, inferenceID, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionClaimNotOwned
	}
	return nil
}

func (s *Postgres) CompleteExecution(ctx context.Context, epochID uint64, escrowID string, inferenceID uint64, ownerID string, fence uint64, result []byte) error {
	queryCtx, cancel := context.WithTimeout(ctx, postgresOpTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `
UPDATE devshard_execution_claims
SET phase = 'completed', result = $6
WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3
	  AND owner_id = $4 AND fence = $5 AND phase = 'dispatched'`,
		epochID, escrowID, inferenceID, ownerID, fence, result)
	if err != nil {
		return fmt.Errorf("complete execution claim %s/%d: %w", escrowID, inferenceID, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionClaimNotOwned
	}
	return nil
}

var _ ExecutionStore = (*Memory)(nil)
var _ ExecutionStore = (*SQLite)(nil)
var _ ExecutionStore = (*Postgres)(nil)
