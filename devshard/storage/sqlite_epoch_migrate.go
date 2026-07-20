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
		Statements: []string{`
CREATE TABLE IF NOT EXISTS sessions (
    escrow_id       TEXT PRIMARY KEY,
    version         TEXT,
    creator_addr    TEXT NOT NULL,
    config_json     TEXT NOT NULL,
    group_json      TEXT NOT NULL,
    initial_balance INTEGER NOT NULL,
    latest_nonce    INTEGER NOT NULL DEFAULT 0,
    last_finalized  INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'active',
    settled_at      INTEGER
)`},
	},
	{
		ID:   2,
		Name: "diffs",
		Statements: []string{`
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
)`},
	},
	{
		ID:   3,
		Name: "signatures",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS signatures (
    escrow_id TEXT NOT NULL,
    nonce     INTEGER NOT NULL,
    slot_id   INTEGER NOT NULL,
    sig       BLOB NOT NULL,
    PRIMARY KEY (escrow_id, nonce, slot_id)
)`},
	},
	{
		ID:   4,
		Name: "snapshots",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS snapshots (
    escrow_id  TEXT PRIMARY KEY,
    nonce      INTEGER NOT NULL,
    state_data BLOB NOT NULL,
    created_at INTEGER NOT NULL DEFAULT 0
)`},
	},
	{
		ID:   5,
		Name: "sealed_inferences",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS sealed_inferences (
    escrow_id            TEXT NOT NULL,
    inference_id         INTEGER NOT NULL,
    sealed_nonce         INTEGER NOT NULL,
    obs_present          INTEGER NOT NULL DEFAULT 0,
    sealed_status        INTEGER NOT NULL DEFAULT 0,
    sealed_executor_slot INTEGER NOT NULL DEFAULT 0,
    sealed_votes_valid   INTEGER NOT NULL DEFAULT 0,
    sealed_votes_invalid INTEGER NOT NULL DEFAULT 0,
    sealed_validated_by  BLOB,
    sealed_model         TEXT NOT NULL DEFAULT '',
    sealed_prompt_hash   BLOB,
    sealed_response_hash BLOB,
    sealed_input_length  INTEGER NOT NULL DEFAULT 0,
    sealed_max_tokens    INTEGER NOT NULL DEFAULT 0,
    sealed_input_tokens  INTEGER NOT NULL DEFAULT 0,
    sealed_output_tokens INTEGER NOT NULL DEFAULT 0,
    sealed_reserved_cost INTEGER NOT NULL DEFAULT 0,
    sealed_actual_cost   INTEGER NOT NULL DEFAULT 0,
    sealed_started_at    INTEGER NOT NULL DEFAULT 0,
    sealed_confirmed_at  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (escrow_id, inference_id)
)`},
	},
	{
		ID:   6,
		Name: "slot_validation_obs",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS slot_validation_obs (
    escrow_id              TEXT NOT NULL,
    slot_id                INTEGER NOT NULL,
    required_validations   INTEGER NOT NULL DEFAULT 0,
    completed_validations  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (escrow_id, slot_id)
)`},
	},
	{
		ID:   7,
		Name: "inference_validation_obs",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS inference_validation_obs (
    escrow_id              TEXT NOT NULL,
    inference_id           INTEGER NOT NULL,
    slot_id                INTEGER NOT NULL,
    required_validations   INTEGER NOT NULL DEFAULT 0,
    completed_validations  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (escrow_id, inference_id, slot_id)
)`,
			`CREATE TABLE IF NOT EXISTS sealed_validation_obs (
    escrow_id              TEXT NOT NULL,
    inference_id           INTEGER NOT NULL,
    slot_id                INTEGER NOT NULL,
    required_validations   INTEGER NOT NULL DEFAULT 0,
    completed_validations  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (escrow_id, inference_id, slot_id)
)`},
	},
	// The SQLite lease store
	// is now a no-op (single-instance; see storage/leases.go),
	// so we skip creating the validation_leases table
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
