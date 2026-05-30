package storage

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// NewStorage builds the canonical Storage for a host process.
//
//   - If PGHOST is unset, use SQLite under sqliteDir.
//   - If PGHOST is set, use sticky hybrid storage: Postgres for new sessions
//     when available, SQLite fallback while Postgres is down, and lazy retry.
func NewStorage(ctx context.Context, sqliteDir string) (Storage, error) {
	pgHost := os.Getenv("PGHOST")
	if pgHost == "" {
		slog.Info("devshard storage: PGHOST not set, using sqlite", "dir", sqliteDir)
		return NewSQLite(sqliteDir)
	}

	sqlite, err := NewSQLite(sqliteDir)
	if err != nil {
		return nil, err
	}

	retryInterval, err := time.ParseDuration(os.Getenv("PG_RETRY_INTERVAL"))
	if err != nil || retryInterval <= 0 {
		retryInterval = 240 * time.Second
	}
	connectTimeout, err := time.ParseDuration(os.Getenv("PG_CONNECT_TIMEOUT"))
	if err != nil || connectTimeout <= 0 {
		connectTimeout = defaultPGConnectTimeout
	}
	return NewHybridStorage(ctx, sqlite, retryInterval, connectTimeout), nil
}
