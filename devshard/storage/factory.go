package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"common/storage/mode"
)

const (
	defaultPGConnectTimeout    = 2 * time.Second
	defaultPGMigrationTimeout  = 2 * time.Minute
	defaultPGIndexTimeout      = 30 * time.Second
	defaultPGReconnectInterval = 5 * time.Second
)

// ErrStoragePGBoundWithoutPostgres is returned when the store directory was
// previously used in Postgres mode but PGHOST is unset at boot.
var ErrStoragePGBoundWithoutPostgres = errors.New(
	"devshard store was previously bound to Postgres; running SQLite-only now would orphan PG sessions. Set PGHOST or delete .pg-bound to override",
)

// ErrStoragePostgresUnavailable is returned for new sessions while running in
// degraded SQLite-only mode because PGHOST is set but Postgres is unreachable.
// In postgres (fail-closed) mode this error also aborts process boot (no degraded fallback).
var ErrStoragePostgresUnavailable = errors.New("devshard postgres storage is configured but unavailable")

// ErrHAPostgresRequired is returned when storage mode needs Postgres but PGHOST is unset.
var ErrHAPostgresRequired = errors.New(
	"devshard storage mode requires Postgres (set PGHOST and PG* env, or set DEVSHARD_STORAGE_MODE=sqlite)",
)

// NewStorage builds the canonical Storage for a host process.
//
// Mode is selected by common/storage/mode (DEVSHARD_STORAGE_MODE, default auto):
//   - sqlite: local SQLite only (.pg-bound guards still apply).
//   - hybrid: PGHOST required; Postgres for new escrows with degraded reconnect;
//     legacy SQLite escrows drain in place.
//   - postgres: PGHOST required; Postgres must be reachable at boot; local SQLite
//     sessions are batch-migrated then quarantined; no SQLite fallback.
//
// See devshard/docs/storage-design.md#storage-mode-selection.
func NewStorage(ctx context.Context, storeDir string) (Storage, error) {
	storageMode, err := mode.Resolve()
	if err != nil {
		return nil, err
	}

	switch storageMode {
	case mode.Postgres:
		return newHAStorage(ctx, storeDir)
	case mode.SQLite:
		if pgHost := os.Getenv("PGHOST"); pgHost != "" {
			slog.Warn("devshard storage: DEVSHARD_STORAGE_MODE=sqlite ignores PGHOST",
				"host", pgHost, "dir", storeDir)
		}
		return newStorageLocal(storeDir)
	case mode.Hybrid:
		if os.Getenv("PGHOST") == "" {
			return nil, ErrHAPostgresRequired
		}
		return newStorageFlexible(ctx, storeDir)
	default:
		return nil, fmt.Errorf("devshard storage: unhandled mode %q", storageMode)
	}
}

func newHAStorage(ctx context.Context, storeDir string) (Storage, error) {
	pgHost := os.Getenv("PGHOST")
	if pgHost == "" {
		return nil, ErrHAPostgresRequired
	}

	pg, err := openPostgresReady(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoragePostgresUnavailable, err)
	}

	sqlite, sqliteDrain, err := openSQLiteDrain(storeDir)
	if err != nil {
		_ = pg.Close()
		return nil, fmt.Errorf("open sqlite for postgres migrate: %w", err)
	}
	if sqliteDrain {
		src, ok := sqlite.(*SQLite)
		if !ok {
			_ = pg.Close()
			_ = sqlite.Close()
			return nil, fmt.Errorf("postgres migrate: unexpected sqlite backend type %T", sqlite)
		}
		n, migErr := MigrateSQLiteSessions(src, pg)
		closeErr := src.Close()
		if migErr != nil {
			_ = pg.Close()
			if closeErr != nil {
				return nil, fmt.Errorf("postgres migrate failed: %w (also close sqlite: %v)", migErr, closeErr)
			}
			return nil, fmt.Errorf("postgres migrate failed: %w", migErr)
		}
		if closeErr != nil {
			_ = pg.Close()
			return nil, fmt.Errorf("close sqlite after postgres migrate: %w", closeErr)
		}
		if err := quarantineSQLiteArtifacts(storeDir); err != nil {
			_ = pg.Close()
			return nil, fmt.Errorf("quarantine sqlite after postgres migrate: %w", err)
		}
		slog.Info("devshard storage: migrated sqlite sessions to postgres",
			"dir", storeDir, "sessions", n)
	}

	router := newHybridRouter(nil, pg, true, storeDir)
	if err := router.reconcilePGBoundAtBoot(); err != nil {
		_ = router.Close()
		return nil, fmt.Errorf("reconcile pg-bound: %w", err)
	}
	slog.Info("devshard storage: postgres-only", "dir", storeDir, "host", pgHost, "mode", mode.Postgres)
	return router, nil
}

// newStorageLocal opens SQLite-only storage (DEVSHARD_STORAGE_MODE=sqlite, or
// auto with PGHOST unset).
func newStorageLocal(storeDir string) (Storage, error) {
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
	slog.Info("devshard storage: using sqlite", "dir", storeDir, "mode", mode.SQLite)
	return newHybridRouter(sqlite, nil, false, storeDir), nil
}

func newStorageFlexible(ctx context.Context, storeDir string) (Storage, error) {
	sqlite, sqliteDrain, err := openSQLiteDrain(storeDir)
	if err != nil {
		return nil, fmt.Errorf("open sqlite drain: %w", err)
	}

	pg, err := openPostgresReady(ctx)
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
	slog.Info("devshard storage: using postgres for new escrows",
		"dir", storeDir, "sqlite_drain", sqliteDrain, "mode", mode.Hybrid)
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
	return newPostgres(ctx, pgConnectTimeout(), pgMigrationTimeout())
}

// openPostgresReady connects under PG_CONNECT_TIMEOUT then WaitReady under
// PG_INDEX_TIMEOUT. Used at boot so migrate / routing never see an empty index.
func openPostgresReady(ctx context.Context) (Storage, error) {
	pg, err := openPostgresWithTimeout(ctx)
	if err != nil {
		return nil, err
	}
	if err := waitPostgresIndexReady(ctx, pg); err != nil {
		_ = pg.Close()
		return nil, err
	}
	return pg, nil
}

type postgresIndexWaiter interface {
	WaitReady(context.Context) error
}

func waitPostgresIndexReady(ctx context.Context, store Storage) error {
	w, ok := store.(postgresIndexWaiter)
	if !ok {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(ctx, pgIndexTimeout())
	defer cancel()
	if err := w.WaitReady(readyCtx); err != nil {
		return err
	}
	return nil
}

func pgConnectTimeout() time.Duration {
	connectTimeout, err := time.ParseDuration(os.Getenv("PG_CONNECT_TIMEOUT"))
	if err != nil || connectTimeout <= 0 {
		return defaultPGConnectTimeout
	}
	return connectTimeout
}

func pgIndexTimeout() time.Duration {
	indexTimeout, err := time.ParseDuration(os.Getenv("PG_INDEX_TIMEOUT"))
	if err != nil || indexTimeout <= 0 {
		return defaultPGIndexTimeout
	}
	return indexTimeout
}

func pgMigrationTimeout() time.Duration {
	timeout, err := time.ParseDuration(os.Getenv("PG_MIGRATION_TIMEOUT"))
	if err != nil || timeout <= 0 {
		return defaultPGMigrationTimeout
	}
	return timeout
}

func pgReconnectInterval() time.Duration {
	interval, err := time.ParseDuration(os.Getenv("PG_RECONNECT_INTERVAL"))
	if err != nil || interval <= 0 {
		return defaultPGReconnectInterval
	}
	return interval
}
