package headertime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS header_times (
  height INTEGER PRIMARY KEY,
  hash BLOB NOT NULL,
  time_ms INTEGER NOT NULL,
  origin TEXT NOT NULL,
  verified INTEGER NOT NULL,
  epoch INTEGER NOT NULL DEFAULT 0
);
`

type sqliteCache struct {
	db *sql.DB
}

func openSQLiteCache(path string) (*sqliteCache, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("header time cache: sqlite path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("header time cache: create sqlite dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteCache{db: db}, nil
}

func (c *sqliteCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *sqliteCache) Load(ctx context.Context) ([]Row, error) {
	if c == nil || c.db == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rs, err := c.db.QueryContext(ctx, `SELECT height, hash, time_ms, origin, verified, epoch FROM header_times ORDER BY height`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		var verified int
		if err := rs.Scan(&r.Height, &r.Hash, &r.TimeMs, &r.Origin, &verified, &r.Epoch); err != nil {
			return nil, err
		}
		r.Verified = verified != 0
		out = append(out, r)
	}
	return out, rs.Err()
}

func (c *sqliteCache) Save(ctx context.Context, rows []Row) error {
	if c == nil || c.db == nil || len(rows) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO header_times (height, hash, time_ms, origin, verified, epoch) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if r.Height == 0 {
			continue
		}
		verified := 0
		if r.Verified {
			verified = 1
		}
		if _, err := stmt.ExecContext(ctx, int64(r.Height), r.Hash, r.TimeMs, string(r.Origin), verified, int64(r.Epoch)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (c *sqliteCache) DeleteBelow(ctx context.Context, floor uint64) error {
	if c == nil || c.db == nil || floor == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := c.db.ExecContext(ctx, `DELETE FROM header_times WHERE height < ?`, int64(floor))
	return err
}
