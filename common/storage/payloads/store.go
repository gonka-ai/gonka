package payloads

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/productscience/inference/x/inference/types"

	"common/logging"
)

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

// Store is the Postgres-backed payload storage.
type Store struct {
	pool        *pgxpool.Pool
	knownEpochs sync.Map
}

type postgresStorage = Store

// New creates a Postgres Store and ensures the payload_storage table exists.
// Prefer Open for devshardd startup; New is used by tests and direct pool wiring.
func New(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	if _, err := pool.Exec(ctx, schema); err != nil {
		return nil, fmt.Errorf("payloads: ensure schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

func newPostgresStorage(ctx context.Context) (*postgresStorage, error) {
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	store, err := New(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	logging.Info("PostgreSQL payload storage initialized", types.PayloadStorage)
	return store, nil
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) ensurePartition(ctx context.Context, epochID uint64) error {
	if _, ok := s.knownEpochs.Load(epochID); ok {
		return nil
	}
	name := fmt.Sprintf("payload_storage_epoch_%d", epochID)
	_, err := s.pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s
		 PARTITION OF payload_storage
		 FOR VALUES FROM (%d) TO (%d)`,
		name, epochID, epochID+1,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P07" {
			s.knownEpochs.Store(epochID, true)
			return nil
		}
		return fmt.Errorf("payloads: ensure partition epoch %d: %w", epochID, err)
	}
	s.knownEpochs.Store(epochID, true)
	logging.Debug("Created partition", types.PayloadStorage, "epochID", epochID)

	return nil
}

func (s *Store) Store(ctx context.Context, escrowID string, inferenceID, epochID uint64, prompt, response []byte) error {
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO payload_storage (escrow_id, inference_id, epoch_id, prompt_payload, response_payload)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (escrow_id, inference_id, epoch_id) DO NOTHING`,
		escrowID, inferenceID, epochID, prompt, response,
	)
	if err != nil {
		return fmt.Errorf("payloads: store: %w", err)
	}
	return nil
}

func (s *Store) Retrieve(ctx context.Context, escrowID string, inferenceID, epochID uint64) (prompt, response []byte, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT prompt_payload, response_payload
		 FROM payload_storage
		 WHERE escrow_id = $1 AND inference_id = $2 AND epoch_id = $3`,
		escrowID, inferenceID, epochID,
	).Scan(&prompt, &response)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("payloads: retrieve %s/%d: %w", escrowID, inferenceID, err)
	}
	return prompt, response, nil
}

func (s *Store) DropEpoch(ctx context.Context, epochID uint64) error {
	partition := pgx.Identifier{fmt.Sprintf("payload_storage_epoch_%d", epochID)}.Sanitize()
	if _, err := s.pool.Exec(ctx, "DROP TABLE IF EXISTS "+partition); err != nil {
		return fmt.Errorf("payloads: drop epoch %d: %w", epochID, err)
	}
	s.knownEpochs.Delete(epochID)
	logging.Info("Dropped epoch partition", types.PayloadStorage, "epochID", epochID)
	return nil
}

var _ Storage = (*Store)(nil)
