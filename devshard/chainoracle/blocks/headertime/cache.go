package headertime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"common/storage/mode"
)

// Cache is the on-disk header-time map. Load restores an already-caught-up
// run after a restart. Save writes heights that were stored. DeleteBelow
// drops the same epoch window the memory map prunes.
type Cache interface {
	Load(ctx context.Context) ([]Row, error)
	Save(ctx context.Context, rows []Row) error
	DeleteBelow(ctx context.Context, floor uint64) error
	Close() error
}

// Row is one persisted header-time entry.
type Row struct {
	Height   uint64
	Hash     []byte
	TimeMs   int64
	Origin   Origin
	Verified bool
	Epoch    uint64
}

// SQLiteFileName is the local file both binaries use under their data dir.
const SQLiteFileName = "headertime.sqlite"

// OpenCache opens the header-time cache selected by DEVSHARD_STORAGE_MODE
// (via common/storage/mode.Resolve):
//
//   - sqlite: local sqlitePath only; PGHOST is ignored
//   - hybrid / postgres: Postgres is required. A local sqlite file is
//     imported once, then the process reads and writes Postgres.
//
// sqlitePath is always the local SQLite file path (used as primary or as
// the import source). An empty path is an error in sqlite mode.
func OpenCache(ctx context.Context, sqlitePath string) (Cache, error) {
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
			slog.Info("header time cache: DEVSHARD_STORAGE_MODE=sqlite ignores PGHOST", "pghost", host)
		}
		return openSQLiteCache(sqlitePath)
	case mode.Hybrid, mode.Postgres:
		host := strings.TrimSpace(os.Getenv("PGHOST"))
		if host == "" {
			return nil, fmt.Errorf("header time cache: mode %s requires PGHOST", storageMode)
		}
		pg, err := openPostgresCache(ctx)
		if err != nil {
			return nil, fmt.Errorf("header time cache: postgres required (mode=%s, host=%s): %w", storageMode, host, err)
		}
		if err := importSQLiteHeaderTimes(ctx, sqlitePath, pg); err != nil {
			_ = pg.Close()
			return nil, err
		}
		slog.Info("header time cache: using postgres", "mode", storageMode, "host", host)
		return pg, nil
	default:
		return nil, fmt.Errorf("header time cache: unhandled mode %q", storageMode)
	}
}

func importSQLiteHeaderTimes(ctx context.Context, sqlitePath string, dst Cache) error {
	if strings.TrimSpace(sqlitePath) == "" {
		return nil
	}
	if _, err := os.Stat(sqlitePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	src, err := openSQLiteCache(sqlitePath)
	if err != nil {
		return fmt.Errorf("header time cache: open sqlite import source: %w", err)
	}
	defer src.Close()
	rows, err := src.Load(ctx)
	if err != nil {
		return fmt.Errorf("header time cache: load sqlite import source: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := dst.Save(ctx, rows); err != nil {
		return fmt.Errorf("header time cache: import sqlite into postgres: %w", err)
	}
	return nil
}
