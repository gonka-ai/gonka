package storage

import (
	"context"
	"database/sql"
	"fmt"

	"devshard/storage/migrate"
)

var sqliteEpochMigrationSteps = []migrate.Step{
	{
		ID:   1,
		Name: "sessions",
		SQL: `
CREATE TABLE IF NOT EXISTS sessions (
    escrow_id       TEXT PRIMARY KEY,
    creator_addr    TEXT NOT NULL,
    config_json     TEXT NOT NULL,
    group_json      TEXT NOT NULL,
    initial_balance INTEGER NOT NULL,
    latest_nonce    INTEGER NOT NULL DEFAULT 0,
    last_finalized  INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'active',
    settled_at      INTEGER
)`,
	},
	{
		ID:   2,
		Name: "diffs",
		SQL: `
CREATE TABLE IF NOT EXISTS diffs (
    escrow_id       TEXT NOT NULL,
    nonce           INTEGER NOT NULL,
    txs_proto       BLOB NOT NULL,
    user_sig        BLOB,
    post_state_root BLOB,
    state_hash      BLOB,
    warm_keys_json  TEXT,
    created_at      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (escrow_id, nonce)
)`,
	},
	{
		ID:   3,
		Name: "signatures",
		SQL: `
CREATE TABLE IF NOT EXISTS signatures (
    escrow_id TEXT NOT NULL,
    nonce     INTEGER NOT NULL,
    slot_id   INTEGER NOT NULL,
    sig       BLOB NOT NULL,
    PRIMARY KEY (escrow_id, nonce, slot_id)
)`,
	},
	{
		ID:   4,
		Name: "snapshots",
		SQL: `
CREATE TABLE IF NOT EXISTS snapshots (
    escrow_id  TEXT PRIMARY KEY,
    nonce      INTEGER NOT NULL,
    state_data BLOB NOT NULL,
    created_at INTEGER NOT NULL DEFAULT 0
)`,
	},
	{
		ID:   5,
		Name: "sealed_inferences",
		SQL: `
CREATE TABLE IF NOT EXISTS sealed_inferences (
    escrow_id    TEXT NOT NULL,
    inference_id INTEGER NOT NULL,
    sealed_nonce INTEGER NOT NULL,
    PRIMARY KEY (escrow_id, inference_id)
)`,
	},
	{
		ID:   6,
		Name: "sessions_version_column",
		SQLiteRun: func(ctx context.Context, tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'version'`,
			).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN version TEXT`)
			return err
		},
	},
	{
		ID:   7,
		Name: "noop",
		SQL:  `SELECT 1`,
	},
}

// MigrateEpochPool applies schema migrations for a per-epoch SQLite file.
func MigrateEpochPool(ctx context.Context, db *sql.DB) error {
	if err := migrate.ApplySQLite(ctx, db, sqliteEpochMigrationSteps); err != nil {
		return fmt.Errorf("devshard sqlite epoch migrate: %w", err)
	}
	return nil
}

// SQLiteEpochMigrationSteps returns a copy of epoch migration steps (for tests).
func SQLiteEpochMigrationSteps() []migrate.Step {
	out := make([]migrate.Step, len(sqliteEpochMigrationSteps))
	copy(out, sqliteEpochMigrationSteps)
	return out
}
