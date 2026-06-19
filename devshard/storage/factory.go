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

type storageBackendKind string

const (
	storageBackendSQLite   storageBackendKind = "sqlite"
	storageBackendPostgres storageBackendKind = "postgres"
)

// NewStorage builds the canonical Storage for a host process.
//
// At boot it selects exactly one backend for the process lifetime:
//   - SQLite when escrow_epoch has rows in _meta.db, or when PGHOST is unset on a
//     fresh store (no .pg-bound marker).
//   - Postgres when PGHOST is set, escrow_epoch is empty, and Postgres connects
//     within PG_CONNECT_TIMEOUT.
//   - Boot fails when .pg-bound exists but PGHOST is unset (would orphan PG sessions).
//
// See devshard/docs/storage-design.md#storage-mode-selection.
func NewStorage(ctx context.Context, storeDir string) (Storage, error) {
	kind, err := decideStorageBackend(storeDir)
	if err != nil {
		return nil, err
	}

	connectTimeout := pgConnectTimeout()

	switch kind {
	case storageBackendSQLite:
		logSQLiteTransitionWarn(storeDir)
		sqlite, err := NewSQLite(storeDir)
		if err != nil {
			return nil, err
		}
		slog.Info("devshard storage: using sqlite", "dir", storeDir)
		return NewHybridStorage(sqlite), nil

	case storageBackendPostgres:
		connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		pg, err := NewPostgres(connectCtx)
		if err != nil {
			return nil, fmt.Errorf("postgres storage: %w", err)
		}
		if err := ensurePGBoundMarker(storeDir); err != nil {
			pg.Close()
			return nil, err
		}
		slog.Info("devshard storage: using postgres", "dir", storeDir)
		return NewHybridStorage(pg), nil

	default:
		return nil, fmt.Errorf("unknown storage backend kind %q", kind)
	}
}

func decideStorageBackend(storeDir string) (storageBackendKind, error) {
	pgHost := os.Getenv("PGHOST")

	hasSQLite, err := HasSQLiteSessions(storeDir)
	if err != nil {
		return "", fmt.Errorf("probe sqlite sessions: %w", err)
	}

	if hasSQLite {
		return storageBackendSQLite, nil
	}

	if pgHost != "" {
		return storageBackendPostgres, nil
	}

	pgBound, err := ReadPGBound(storeDir)
	if err != nil {
		return "", fmt.Errorf("read pg-bound marker: %w", err)
	}
	if pgBound {
		return "", ErrStoragePGBoundWithoutPostgres
	}

	return storageBackendSQLite, nil
}

func ensurePGBoundMarker(storeDir string) error {
	pgBound, err := ReadPGBound(storeDir)
	if err != nil {
		return err
	}
	if pgBound {
		return nil
	}
	return WritePGBound(storeDir)
}

func pgConnectTimeout() time.Duration {
	connectTimeout, err := time.ParseDuration(os.Getenv("PG_CONNECT_TIMEOUT"))
	if err != nil || connectTimeout <= 0 {
		return defaultPGConnectTimeout
	}
	return connectTimeout
}

func logSQLiteTransitionWarn(storeDir string) {
	if os.Getenv("PGHOST") == "" {
		return
	}
	hasSQLite, err := HasSQLiteSessions(storeDir)
	if err != nil || !hasSQLite {
		return
	}
	slog.Warn(
		"devshard storage: draining sqlite sessions while PGHOST is set; settle and prune until escrow_epoch is empty, then restart for postgres-only mode",
		"dir", storeDir,
	)
}
