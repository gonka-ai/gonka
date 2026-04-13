// Package storage provides PostgreSQL-backed stats persistence for inference-api.
// Payload and claim storage live in common/storage/payloads and common/storage/claims.
package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Storage holds a shared connection pool. Only stats methods remain at this level.
type Storage struct {
	pool *pgxpool.Pool
}

// New opens a connection pool and ensures the stats schema exists.
func New(ctx context.Context, dsn string) (*Storage, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open pool: %w", err)
	}
	s := &Storage{pool: pool}
	if err := s.ensureStatsSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases all connections in the pool.
func (s *Storage) Close() {
	s.pool.Close()
}
