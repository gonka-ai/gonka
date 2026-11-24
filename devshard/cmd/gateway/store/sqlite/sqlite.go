// Package sqlite is the gateway store on a local SQLite file, <storageDir>/gateway.db. See ../README.md, "SQLite".
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"devshard/cmd/gateway/store"
	"devshard/cmd/gateway/store/internal/rows"
)

var _ store.Backend = (*Backend)(nil)

// Backend is the SQLite backend; Open wraps it with the ledger and owed-vote writers.
type Backend struct {
	db      *sql.DB
	retrier rows.Retrier
}

// Open creates the storage directory if needed, opens <storageDir>/gateway.db and applies the migrations. See ../README.md, "SQLite".
func Open(storageDir string) (store.Store, error) {
	backend, err := openBackend(storageDir)
	if err != nil {
		return nil, err
	}
	return store.New(backend), nil
}

// DatabasePath is where Open keeps the database inside storageDir.
func DatabasePath(storageDir string) string {
	return filepath.Join(storageDir, gatewayDatabaseFileName)
}

func openBackend(storageDir string) (*Backend, error) {
	if err := os.MkdirAll(storageDir, storageDirectoryMode); err != nil {
		return nil, fmt.Errorf("creating storage dir: %w", err)
	}
	databasePath := DatabasePath(storageDir)
	db, err := sql.Open("sqlite", databasePath+connectionPragmas)
	if err != nil {
		return nil, fmt.Errorf("opening gateway store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := refuseLegacyDatabase(db, databasePath); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Backend{db: db, retrier: rows.Retrier{Retryable: isLockedError, Backoff: rows.DefaultRetryBackoff}}, nil
}

// WithRetry retries only a locked database. See ../README.md, "Writing under a lock".
func (s *Backend) WithRetry(ctx context.Context, operation func() error) error {
	return s.retrier.Do(ctx, operation)
}

// Close closes the database handle.
func (s *Backend) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("closing gateway store: %w", err)
	}
	return nil
}

func refuseLegacyDatabase(db *sql.DB, databasePath string) error {
	present, err := tableExists(db, "schema_version")
	if err != nil || present {
		return err
	}
	for _, table := range legacyOnlyTables {
		switch present, err := tableExists(db, table); {
		case err != nil:
			return err
		case present:
			return fmt.Errorf("opening %s: %w", databasePath, ErrLegacyDatabase)
		}
	}
	return nil
}

func tableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspecting table %q: %w", table, err)
	}
	return true, nil
}
