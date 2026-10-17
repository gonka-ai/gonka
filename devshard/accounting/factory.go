package accounting

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"common/storage/mode"
)

// OpenStoreContext opens the accounting backend selected by DEVSHARD_STORAGE_MODE
// (via common/storage/mode.Resolve):
//
//   - sqlite: local accounting.db only; PGHOST is ignored
//   - hybrid / postgres: Postgres is required (no SQLite runtime fallback).
//     Local accounting.db is imported before the store serves. Open fails when
//     Postgres is unreachable.
//
// Session storage keeps its own degraded SQLite path; epoch accounting does
// not — when PGHOST selects Postgres, the ledger is Postgres-only.
//
// sqlitePath is always the local SQLite file path (used as primary or migration source).
func OpenStoreContext(ctx context.Context, sqlitePath string, retention uint64) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	storageMode, err := mode.Resolve()
	if err != nil {
		return nil, err
	}

	switch storageMode {
	case mode.SQLite:
		if host := strings.TrimSpace(os.Getenv("PGHOST")); host != "" {
			log.Printf("accounting store: DEVSHARD_STORAGE_MODE=sqlite ignores PGHOST (%s)", host)
		}
		return openSQLiteStore(sqlitePath, retention)

	case mode.Hybrid, mode.Postgres:
		host := strings.TrimSpace(os.Getenv("PGHOST"))
		if host == "" {
			return nil, fmt.Errorf("accounting store: mode %s requires PGHOST", storageMode)
		}
		store, err := openPostgresStore(ctx, sqlitePath, retention,
			fmt.Sprintf("accounting store: postgres required (mode=%s, host=%s)", storageMode, host))
		if err != nil {
			return nil, err
		}
		log.Printf("accounting store: using postgres only (mode=%s, host=%s)", storageMode, host)
		return store, nil

	default:
		return nil, fmt.Errorf("accounting store: unhandled mode %q", storageMode)
	}
}

// openPostgresStore opens a Postgres-only backend, importing local SQLite state
// first. The connection is mandatory; a failure aborts the open with failMsg as context.
func openPostgresStore(ctx context.Context, sqlitePath string, retention uint64, failMsg string) (*Store, error) {
	pg, err := openPostgresBackend(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", failMsg, err)
	}
	if err := migrateSQLiteAccountingToPostgres(ctx, sqlitePath, pg); err != nil {
		_ = pg.Close()
		return nil, err
	}
	return &Store{backend: pg, retention: retention, path: sqlitePath}, nil
}

func openSQLiteStore(sqlitePath string, retention uint64) (*Store, error) {
	backend, err := openSQLiteBackend(sqlitePath)
	if err != nil {
		return nil, err
	}
	return &Store{backend: backend, retention: retention, path: sqlitePath}, nil
}
