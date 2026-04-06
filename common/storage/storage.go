// Package storage provides PostgreSQL-backed persistence for inference-api.
// All database operations are methods on the single Storage struct.
package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Storage holds a shared connection pool. All table operations are methods on this struct.
// Schema files (payloads.go, stats.go, claims.go) each contribute their own methods.
type Storage struct {
	pool *pgxpool.Pool
}

// New opens a connection pool to the PostgreSQL database at dsn and ensures
// the schema exists.
func New(ctx context.Context, dsn string) (*Storage, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open pool: %w", err)
	}
	s := &Storage{pool: pool}
	if err := s.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Storage) ensureSchema(ctx context.Context) error {
	for _, fn := range []func(context.Context) error{
		s.ensurePayloadsSchema,
		s.ensureStatsSchema,
		s.ensureClaimsSchema,
	} {
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close releases all connections in the pool.
func (s *Storage) Close() {
	s.pool.Close()
}
