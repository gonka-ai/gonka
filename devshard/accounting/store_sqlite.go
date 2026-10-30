package accounting

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type sqliteBackend struct {
	db *sql.DB
}

func openSQLiteBackend(path string) (*sqliteBackend, error) {
	if err := requirePath(path); err != nil {
		return nil, err
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
	return &sqliteBackend{db: db}, nil
}

func (s *sqliteBackend) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *sqliteBackend) Load(ctx context.Context, t *Tracker) error {
	if s == nil || s.db == nil || t == nil {
		return nil
	}
	metaRows, err := s.db.QueryContext(ctx, `SELECT key, value FROM accounting_meta`)
	if err != nil {
		return err
	}
	for metaRows.Next() {
		var key, value string
		if err := metaRows.Scan(&key, &value); err != nil {
			metaRows.Close()
			return err
		}
		applyLoadedMeta(t, key, value)
	}
	if err := metaRows.Err(); err != nil {
		metaRows.Close()
		return err
	}
	if err := metaRows.Close(); err != nil {
		return err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM accounting_escrows`)
	if err != nil {
		return err
	}
	defer rows.Close()

	t.mu.Lock()
	defer t.mu.Unlock()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		blob, err := decodeEscrowBlob(raw)
		if err != nil {
			return err
		}
		if err := applyLoadedEscrow(t, blob); err != nil {
			return err
		}
	}
	t.dirty = nil
	return rows.Err()
}

func (s *sqliteBackend) Save(ctx context.Context, snap storeSnapshot, _, _ []string) error {
	if s == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounting_escrows`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounting_meta`); err != nil {
		return err
	}
	schemaKey, schemaVal := schemaVersionMeta()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO accounting_meta(key, value) VALUES
		 (?, ?), (?, ?), (?, ?)`,
		schemaKey, schemaVal,
		"updated_at", snap.UpdatedAt.Format(time.RFC3339Nano),
		"writer_errors", snap.WriterErrors,
	); err != nil {
		return err
	}
	for _, blob := range snap.Escrows {
		raw, err := encodeEscrowBlob(blob)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO accounting_escrows(escrow_id, creation_epoch, model, payload)
			 VALUES (?, ?, ?, ?)`,
			blob.Meta.EscrowID, blob.Meta.CreationEpoch, blob.Meta.Model, raw,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS accounting_meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounting_escrows (
	escrow_id TEXT PRIMARY KEY,
	creation_epoch INTEGER NOT NULL,
	model TEXT NOT NULL,
	payload BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS accounting_escrows_epoch_model_idx
	ON accounting_escrows(creation_epoch, model);
`
