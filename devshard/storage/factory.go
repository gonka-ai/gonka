package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

const defaultPGConnectTimeout = 2 * time.Second

// ErrStoragePGBoundWithoutPostgres is returned when the store directory was
// previously used in Postgres mode but PGHOST is unset at boot.
var ErrStoragePGBoundWithoutPostgres = errors.New(
	"devshard store was previously bound to Postgres; running SQLite-only now would orphan PG sessions. Set PGHOST or delete .pg-bound to override",
)

// NewStorage builds the canonical Storage for a host process.
//
// The returned store is a per-session router (HybridStorage):
//   - When PGHOST is unset it is SQLite-only. Boot fails when a .pg-bound marker
//     exists (running SQLite-only would orphan the store's Postgres sessions).
//   - When PGHOST is set, Postgres is the backend for all new escrows and must
//     connect within PG_CONNECT_TIMEOUT (boot fails otherwise). If the store
//     already holds legacy SQLite escrows, SQLite is attached alongside Postgres
//     so those escrows keep being served and drain in place as they settle and
//     prune — new escrows still go to Postgres.
//
// A given escrow lives in exactly one backend: CreateSession picks one backend
// and never falls back, so append logs cannot fork across backends.
//
// See devshard/docs/storage-design.md#storage-mode-selection.
func NewStorage(ctx context.Context, storeDir string) (Storage, error) {
	pgHost := os.Getenv("PGHOST")

	hasSQLite, err := HasSQLiteSessions(storeDir)
	if err != nil {
		return nil, fmt.Errorf("probe sqlite sessions: %w", err)
	}

	if pgHost == "" {
		pgBound, err := ReadPGBound(storeDir)
		if err != nil {
			return nil, fmt.Errorf("read pg-bound marker: %w", err)
		}
		if pgBound {
			return nil, ErrStoragePGBoundWithoutPostgres
		}
		sqlite, err := NewSQLite(storeDir)
		if err != nil {
			return nil, err
		}
		slog.Info("devshard storage: using sqlite", "dir", storeDir)
		return newHybridRouter(sqlite, nil, false, storeDir), nil
	}

	connectCtx, cancel := context.WithTimeout(ctx, pgConnectTimeout())
	defer cancel()
	pg, err := NewPostgres(connectCtx)
	if err != nil {
		return nil, fmt.Errorf("postgres storage: %w", err)
	}

	var sqlite Storage
	if hasSQLite {
		s, err := NewSQLite(storeDir)
		if err != nil {
			pg.Close()
			return nil, err
		}
		sqlite = s
		slog.Warn(
			"devshard storage: serving legacy sqlite escrows alongside postgres; they drain in place as they settle and prune while new escrows go to postgres",
			"dir", storeDir,
		)
	}

	router := newHybridRouter(sqlite, pg, true, storeDir)
	// Align .pg-bound with reality: present only while PG holds sessions. The
	// marker is written ahead of each new PG session and cleared once PG drains.
	if err := router.reconcilePGBoundAtBoot(); err != nil {
		_ = router.Close()
		return nil, fmt.Errorf("reconcile pg-bound: %w", err)
	}
	slog.Info("devshard storage: using postgres for new escrows", "dir", storeDir, "sqlite_drain", hasSQLite)
	return router, nil
}

func pgConnectTimeout() time.Duration {
	connectTimeout, err := time.ParseDuration(os.Getenv("PG_CONNECT_TIMEOUT"))
	if err != nil || connectTimeout <= 0 {
		return defaultPGConnectTimeout
	}
	return connectTimeout
}
