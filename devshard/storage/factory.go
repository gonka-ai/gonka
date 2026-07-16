package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

const (
	defaultPGConnectTimeout    = 2 * time.Second
	defaultPGReconnectInterval = 5 * time.Second
)

// ErrStoragePGBoundWithoutPostgres is returned when the store directory was
// previously used in Postgres mode but PGHOST is unset at boot.
var ErrStoragePGBoundWithoutPostgres = errors.New(
	"devshard store was previously bound to Postgres; running SQLite-only now would orphan PG sessions. Set PGHOST or delete .pg-bound to override",
)

// ErrStoragePostgresUnavailable is returned for new sessions while running in
// degraded SQLite-only mode because PGHOST is set but Postgres is unreachable.
var ErrStoragePostgresUnavailable = errors.New("devshard postgres storage is configured but unavailable")

// NewStorage builds the canonical Storage for a host process.
//
// The returned store is a per-session router (HybridStorage):
//   - When PGHOST is unset it is SQLite-only. If .pg-bound exists and SQLite
//     still has sessions, boot enters degraded SQLite-owned-only mode: existing
//     SQLite escrows are served, but new/unknown escrows are rejected because
//     they may belong to unavailable Postgres.
//   - When PGHOST is set, Postgres is the backend for all new escrows. If
//     Postgres is temporarily unavailable, boot enters degraded mode instead of
//     taking the whole process down.
//   - When PGHOST is set and Postgres connects, legacy SQLite escrows are
//     attached alongside Postgres so they keep being served and drain in place
//     while new escrows go to Postgres.
//
// A given escrow lives in exactly one backend: CreateSession picks one backend
// and never falls back to SQLite for Postgres-destined new escrows, so append
// logs cannot fork across backends.
//
// See devshard/docs/storage-design.md#storage-mode-selection.
func NewStorage(ctx context.Context, storeDir string) (Storage, error) {
	pgHost := os.Getenv("PGHOST")

	if pgHost == "" {
		pgBound, err := ReadPGBound(storeDir)
		if err != nil {
			return nil, fmt.Errorf("read pg-bound marker: %w", err)
		}
		if pgBound {
			sqlite, sqliteDrain, err := openSQLiteDrain(storeDir)
			if err != nil {
				return nil, fmt.Errorf("open sqlite degraded: %w", err)
			}
			if sqliteDrain {
				slog.Warn(
					"devshard storage: .pg-bound present but PGHOST unset; serving sqlite-owned escrows only and rejecting new escrows",
					"dir", storeDir,
				)
				return newDegradedSQLiteRouter(sqlite, storeDir, ErrStoragePGBoundWithoutPostgres), nil
			}
			return nil, ErrStoragePGBoundWithoutPostgres
		}
		sqlite, err := NewSQLite(storeDir)
		if err != nil {
			return nil, err
		}
		slog.Info("devshard storage: using sqlite", "dir", storeDir)
		return newHybridRouter(sqlite, nil, false, storeDir), nil
	}

	sqlite, sqliteDrain, err := openSQLiteDrain(storeDir)
	if err != nil {
		return nil, fmt.Errorf("open sqlite drain: %w", err)
	}

	pg, err := openPostgresWithTimeout(ctx)
	if err != nil {
		slog.Warn(
			"devshard storage: postgres unavailable; entering degraded mode while reconnect runs",
			"dir", storeDir,
			"sqlite_drain", sqliteDrain,
			"error", err,
		)
		router := newDegradedSQLiteRouter(sqlite, storeDir, fmt.Errorf("%w: %w", ErrStoragePostgresUnavailable, err))
		router.startPostgresReconnect(ctx, openPostgresWithTimeout, pgReconnectInterval())
		return router, nil
	}

	if sqliteDrain {
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
	router.logConflictedEscrows("boot")
	slog.Info("devshard storage: using postgres for new escrows", "dir", storeDir, "sqlite_drain", sqliteDrain)
	return router, nil
}

func openSQLiteDrain(storeDir string) (Storage, bool, error) {
	hasSQLiteArtifacts, err := HasSQLiteArtifacts(storeDir)
	if err != nil {
		return nil, false, fmt.Errorf("probe sqlite artifacts: %w", err)
	}
	if !hasSQLiteArtifacts {
		return nil, false, nil
	}
	s, err := NewSQLite(storeDir)
	if err != nil {
		return nil, false, err
	}
	if s.HasAnySessions() {
		return s, true, nil
	}
	if err := s.Close(); err != nil {
		return nil, false, fmt.Errorf("close empty sqlite store: %w", err)
	}
	return nil, false, nil
}

func openPostgresWithTimeout(ctx context.Context) (Storage, error) {
	connectCtx, cancel := context.WithTimeout(ctx, pgConnectTimeout())
	defer cancel()
	return NewPostgres(connectCtx)
}

func pgConnectTimeout() time.Duration {
	connectTimeout, err := time.ParseDuration(os.Getenv("PG_CONNECT_TIMEOUT"))
	if err != nil || connectTimeout <= 0 {
		return defaultPGConnectTimeout
	}
	return connectTimeout
}

func pgReconnectInterval() time.Duration {
	interval, err := time.ParseDuration(os.Getenv("PG_RECONNECT_INTERVAL"))
	if err != nil || interval <= 0 {
		return defaultPGReconnectInterval
	}
	return interval
}
