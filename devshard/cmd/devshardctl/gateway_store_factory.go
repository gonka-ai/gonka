package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"common/storage/mode"
	"common/storage/pgtimeouts"
)

// NewGatewayStore opens the gateway persistence layer for the mode selected by
// DEVSHARD_STORAGE_MODE (via common/storage/mode.Resolve):
//
//   - sqlite: local gateway.db only; PGHOST is ignored
//   - hybrid / postgres: Postgres is required (no SQLite runtime fallback).
//     Local gateway.db is imported and any leftover sync-journal rows are
//     drained before the store serves. Open fails when Postgres is unreachable.
//
// Session storage keeps its own degraded SQLite path; gateway management state
// does not — when PGHOST selects Postgres, the gateway store is Postgres-only.
func NewGatewayStore(ctx context.Context, baseStorageDir string) (GatewayStore, error) {
	storageMode, err := mode.Resolve()
	if err != nil {
		return nil, err
	}
	sqlitePath := filepath.Join(baseStorageDir, "gateway.db")
	pgHost := strings.TrimSpace(os.Getenv("PGHOST"))

	if !storageMode.RequiresPGHOST() {
		if pgHost != "" {
			log.Printf("gateway store: %s=%s ignores PGHOST (%s)", mode.EnvStorageMode, storageMode, pgHost)
		}
		return NewSQLiteGatewayStore(sqlitePath)
	}
	if pgHost == "" {
		return nil, fmt.Errorf("gateway store: mode %s requires PGHOST", storageMode)
	}

	pg, pgErr := NewPostgresGatewayStore(ctx)
	if pgErr != nil {
		return nil, fmt.Errorf("gateway store: postgres required (mode=%s, host=%s): %w", storageMode, pgHost, pgErr)
	}

	if err := importGatewaySQLite(ctx, sqlitePath, pg); err != nil {
		_ = pg.Close()
		return nil, err
	}

	log.Printf("gateway store: using postgres only (mode=%s, host=%s)", storageMode, pgHost)
	return pg, nil
}

// importGatewaySQLite migrates a pre-existing local gateway.db into Postgres and
// replays sync-journal rows left by an earlier hybrid deployment. SQLite is only
// a migration source here: it is opened read-write for the journal drain, then
// closed so runtime never writes locally. A missing file means nothing to import.
func importGatewaySQLite(ctx context.Context, sqlitePath string, pg *PostgresGatewayStore) error {
	if _, err := os.Stat(sqlitePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("gateway store: stat %s: %w", sqlitePath, err)
	}

	sqlite, err := NewSQLiteGatewayStore(sqlitePath)
	if err != nil {
		return err
	}
	defer func() { _ = sqlite.Close() }()

	// Bounded so a slow Postgres cannot hang boot forever, but generous: a large
	// journal or commitment history is replayed row by row.
	importCtx, cancel := context.WithTimeout(ctx, pgtimeouts.ImportTimeout())
	defer cancel()

	if err := MigrateGatewaySQLiteToPostgres(importCtx, sqlite, pg); err != nil {
		return err
	}
	if err := drainGatewaySyncJournalUntilEmpty(importCtx, sqlite, pg); err != nil {
		return fmt.Errorf("gateway store: sync journal drain failed: %w", err)
	}
	return nil
}
