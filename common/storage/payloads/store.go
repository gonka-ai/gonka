package payloads

import (
	"common/logging"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/productscience/inference/x/inference/types"
)

// ErrNotFound is returned when a requested payload does not exist.
var ErrNotFound = errors.New("payloads: not found")

const schema = `
CREATE TABLE IF NOT EXISTS payload_storage (
    escrow_id        TEXT   NOT NULL,
    inference_id     INT   NOT NULL,
    epoch_id         BIGINT NOT NULL,
    prompt_payload   BYTEA,
    response_payload BYTEA,
    PRIMARY KEY (escrow_id, inference_id, epoch_id)
) PARTITION BY RANGE (epoch_id);
`

// Store holds a shared connection pool for the payload_storage table.
type Store struct {
	pool        *pgxpool.Pool
	knownEpochs sync.Map
}

// New creates a Store and ensures the payload_storage table exists.
func New(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return nil, fmt.Errorf("payloads: ensure schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// ensurePartition creates the epoch partition if it does not already exist.
// Note: concurrent writes to the same epoch may race on the CREATE TABLE — this is
// acceptable since IF NOT EXISTS makes it idempotent and any error is retried by the caller.
func (s *Store) ensurePartition(ctx context.Context, epochId uint64) error {
	if _, ok := s.knownEpochs.Load(epochId); ok {
		return nil
	}
	name := fmt.Sprintf("payload_storage_epoch_%d", epochId)
	_, err := s.pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s
		 PARTITION OF payload_storage
		 FOR VALUES FROM (%d) TO (%d)`,
		name, epochId, epochId+1,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P07" {
			s.knownEpochs.Store(epochId, true)
			return nil
		}
		return fmt.Errorf("payloads: ensure partition epoch %d: %w", epochId, err)
	}
	s.knownEpochs.Store(epochId, true)
	logging.Debug("Created partition", types.PayloadStorage, "epochId", epochId)

	return nil
}

// Store persists the prompt and response payloads for an inference.
// The table is partitioned by epoch_id; partitions are created lazily on first write.
func (s *Store) Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, prompt, response []byte) error {
	if err := s.ensurePartition(ctx, epochId); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO payload_storage (escrow_id, inference_id, epoch_id, prompt_payload, response_payload)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (escrow_id, inference_id, epoch_id) DO NOTHING`,
		escrowId, inferenceId, epochId, prompt, response,
	)
	if err != nil {
		return fmt.Errorf("payloads: store: %w", err)
	}
	return nil
}

// Retrieve fetches the stored prompt and response for an inference.
func (s *Store) Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) (prompt, response []byte, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT prompt_payload, response_payload
		 FROM payload_storage
		 WHERE escrow_id = $1 AND inference_id = $2 AND epoch_id = $3`,
		escrowId, inferenceId, epochId,
	).Scan(&prompt, &response)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("payloads: retrieve %s/%d: %w", escrowId, inferenceId, err)
	}
	return prompt, response, nil
}

// DropEpoch drops the partition for exactly one epoch. Idempotent.
func (s *Store) DropEpoch(ctx context.Context, epochId uint64) error {
	partition := pgx.Identifier{fmt.Sprintf("payload_storage_epoch_%d", epochId)}.Sanitize()
	if _, err := s.pool.Exec(ctx, "DROP TABLE IF EXISTS "+partition); err != nil {
		return fmt.Errorf("payloads: drop epoch %d: %w", epochId, err)
	}
	s.knownEpochs.Delete(epochId)
	logging.Info("Dropped epoch partition", types.PayloadStorage, "epochId", epochId)
	return nil
}
