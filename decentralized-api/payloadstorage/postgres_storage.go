package payloadstorage

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"decentralized-api/logging"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/productscience/inference/x/inference/types"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS inferences (
    epoch_id BIGINT NOT NULL,
    inference_id TEXT NOT NULL,
    prompt_payload BYTEA,
    response_payload BYTEA,
    prompt_hash TEXT,
    response_hash TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (epoch_id, inference_id)
) PARTITION BY RANGE (epoch_id)
`

type PostgresStorage struct {
	pool        *pgxpool.Pool
	knownEpochs sync.Map
}

// NewPostgresStorage creates a new PostgreSQL storage using standard libpq env vars.
// Environment variables: PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD
func NewPostgresStorage(ctx context.Context) (*PostgresStorage, error) {
	// pgxpool.New automatically reads from environment variables
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	s := &PostgresStorage{pool: pool}

	if err := s.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ensure schema: %w", err)
	}

	logging.Info("PostgreSQL storage initialized", types.PayloadStorage)
	return s, nil
}

func (s *PostgresStorage) ensureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, createTableSQL)
	if err != nil {
		return fmt.Errorf("create parent table: %w", err)
	}
	return nil
}

func (s *PostgresStorage) ensurePartition(ctx context.Context, epochId uint64) error {
	if _, ok := s.knownEpochs.Load(epochId); ok {
		return nil
	}

	tableName := fmt.Sprintf("inferences_epoch_%d", epochId)
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s 
		PARTITION OF inferences 
		FOR VALUES FROM (%d) TO (%d)
	`, tableName, epochId, epochId+1)

	_, err := s.pool.Exec(ctx, query)
	if err != nil {
		// Handle race condition: table already exists (error code 42P07)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P07" {
			s.knownEpochs.Store(epochId, true)
			return nil
		}
		return fmt.Errorf("create partition %s: %w", tableName, err)
	}

	s.knownEpochs.Store(epochId, true)
	logging.Debug("Created partition", types.PayloadStorage, "epochId", epochId)
	return nil
}

func (s *PostgresStorage) Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error {
	if err := s.ensurePartition(ctx, epochId); err != nil {
		return err
	}

	// Compute hashes for debugging
	promptHash, err := ComputePromptHash(promptPayload)
	if err != nil {
		logging.Debug("Failed to compute prompt hash", types.PayloadStorage, "error", err)
		promptHash = ""
	}

	responseHash, err := ComputeResponseHash(responsePayload)
	if err != nil {
		logging.Debug("Failed to compute response hash", types.PayloadStorage, "error", err)
		responseHash = ""
	}

	query := `
		INSERT INTO inferences (epoch_id, inference_id, prompt_payload, response_payload, prompt_hash, response_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (epoch_id, inference_id) DO NOTHING
	`
	_, err = s.pool.Exec(ctx, query, epochId, inferenceId, promptPayload, responsePayload, promptHash, responseHash)
	if err != nil {
		return fmt.Errorf("insert payload: %w", err)
	}

	logging.Debug("Stored payload in PostgreSQL", types.PayloadStorage, "inferenceId", inferenceId, "epochId", epochId)
	return nil
}

func (s *PostgresStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	query := `
		SELECT prompt_payload, response_payload 
		FROM inferences 
		WHERE epoch_id = $1 AND inference_id = $2
	`

	var prompt, response []byte
	err := s.pool.QueryRow(ctx, query, epochId, inferenceId).Scan(&prompt, &response)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("query payload: %w", err)
	}

	return prompt, response, nil
}

func (s *PostgresStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	tableName := fmt.Sprintf("inferences_epoch_%d", epochId)
	query := fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName)

	_, err := s.pool.Exec(ctx, query)
	if err != nil {
		return fmt.Errorf("drop partition %s: %w", tableName, err)
	}

	s.knownEpochs.Delete(epochId)
	logging.Info("Pruned epoch partition", types.PayloadStorage, "epochId", epochId)
	return nil
}

func (s *PostgresStorage) Close() {
	s.pool.Close()
}

var _ PayloadStorage = (*PostgresStorage)(nil)
