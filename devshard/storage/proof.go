package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ProofOperation is an operation used by deployment preflight to prove that
// independent devshard processes use the same live writable PostgreSQL.
type ProofOperation string

const (
	ProofIdentity       ProofOperation = "identity"
	ProofWriteChallenge ProofOperation = "write"
	ProofReadChallenge  ProofOperation = "read"
)

// StorageProof reports the durable lineage marker and whether a challenge was
// observed through this storage handle. Identity alone is not proof of a shared
// live database because snapshots and replicas preserve it.
type StorageProof struct {
	Identity string `json:"identity"`
	Found    bool   `json:"found,omitempty"`
}

// ProofProvider is implemented by storage wrappers that can reach the actual
// PostgreSQL backend used for devshard session traffic.
type ProofProvider interface {
	StorageProof(context.Context, ProofOperation, string) (StorageProof, error)
}

var errPostgresProofUnavailable = errors.New("postgres storage proof is unavailable")

// StorageProof performs the proof through the application pool. Every
// operation rejects a standby; write additionally proves that this connection
// can mutate the database.
func (s *Postgres) StorageProof(ctx context.Context, operation ProofOperation, nonce string) (StorageProof, error) {
	switch operation {
	case ProofIdentity, ProofWriteChallenge, ProofReadChallenge:
	default:
		return StorageProof{}, fmt.Errorf("unknown storage proof operation %q", operation)
	}
	if operation != ProofIdentity {
		if _, err := uuid.Parse(nonce); err != nil {
			return StorageProof{}, fmt.Errorf("invalid storage challenge nonce: %w", err)
		}
	}
	if s == nil || s.pool == nil {
		return StorageProof{}, errPostgresProofUnavailable
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return StorageProof{}, fmt.Errorf("begin storage proof: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), postgresStatementTimeout)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	var inRecovery bool
	if err := tx.QueryRow(ctx, `SELECT pg_is_in_recovery()`).Scan(&inRecovery); err != nil {
		return StorageProof{}, fmt.Errorf("check postgres recovery state: %w", err)
	}
	if inRecovery {
		return StorageProof{}, errors.New("postgres storage is a recovery replica")
	}

	proof := StorageProof{}
	switch operation {
	case ProofIdentity:
		err = tx.QueryRow(ctx, `
			SELECT identity::text
			FROM devshard_storage_identity
			WHERE singleton`).Scan(&proof.Identity)
	case ProofWriteChallenge:
		err = tx.QueryRow(ctx, `
			UPDATE devshard_storage_identity
			SET challenge = $1::uuid, challenged_at = NOW()
			WHERE singleton
			RETURNING identity::text`, nonce).Scan(&proof.Identity)
		proof.Found = err == nil
	case ProofReadChallenge:
		err = tx.QueryRow(ctx, `
			SELECT identity::text, COALESCE(challenge = $1::uuid, FALSE)
			FROM devshard_storage_identity
			WHERE singleton`, nonce).Scan(&proof.Identity, &proof.Found)
	}
	if err != nil {
		return StorageProof{}, fmt.Errorf("perform storage proof %s: %w", operation, err)
	}
	if proof.Identity == "" {
		return StorageProof{}, errors.New("postgres storage identity is empty")
	}
	if err := tx.Commit(ctx); err != nil {
		return StorageProof{}, fmt.Errorf("commit storage proof: %w", err)
	}
	return proof, nil
}

// StorageProof forwards through the currently attached PostgreSQL backend.
func (h *HybridStorage) StorageProof(ctx context.Context, operation ProofOperation, nonce string) (StorageProof, error) {
	pg := h.postgresBackend()
	provider, ok := pg.(ProofProvider)
	if !ok {
		return StorageProof{}, errPostgresProofUnavailable
	}
	return provider.StorageProof(ctx, operation, nonce)
}

// StorageProof preserves the proof capability through the retention wrapper.
func (m *ManagedStorage) StorageProof(ctx context.Context, operation ProofOperation, nonce string) (StorageProof, error) {
	provider, ok := m.inner.(ProofProvider)
	if !ok {
		return StorageProof{}, errPostgresProofUnavailable
	}
	return provider.StorageProof(ctx, operation, nonce)
}
