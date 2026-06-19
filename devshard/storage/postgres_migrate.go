package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"devshard/storage/migrate"
)

// postgresMigrationSteps is the ordered forward-only schema for devshard Postgres parents.
// Per-epoch partitions are created lazily via ensurePartition only.
var postgresMigrationSteps = []migrate.Step{
	{
		ID:   1,
		Name: "devshard_session_index",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_session_index (
    escrow_id TEXT   PRIMARY KEY,
    epoch_id  BIGINT NOT NULL
)`},
	},
	{
		ID:         2,
		Name:       "devshard_session_index_by_epoch",
		Statements: []string{`CREATE INDEX IF NOT EXISTS devshard_session_index_by_epoch ON devshard_session_index(epoch_id)`},
	},
	{
		ID:   3,
		Name: "devshard_sessions_parent",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_sessions (
    epoch_id        BIGINT NOT NULL,
    escrow_id       TEXT   NOT NULL,
    version         TEXT,
    creator_addr    TEXT   NOT NULL,
    config_json     TEXT   NOT NULL,
    group_json      TEXT   NOT NULL,
    initial_balance BIGINT NOT NULL,
    latest_nonce    BIGINT NOT NULL DEFAULT 0,
    last_finalized  BIGINT NOT NULL DEFAULT 0,
    status          TEXT   NOT NULL DEFAULT 'active',
    settled_at      BIGINT,
    PRIMARY KEY (epoch_id, escrow_id)
) PARTITION BY RANGE (epoch_id)`},
	},
	{
		ID:   4,
		Name: "devshard_diffs_parent",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_diffs (
    epoch_id        BIGINT NOT NULL,
    escrow_id       TEXT   NOT NULL,
    nonce           BIGINT NOT NULL,
    txs_proto       BYTEA  NOT NULL,
    user_sig        BYTEA,
    post_state_root BYTEA,
    state_hash      BYTEA,
    warm_keys_json  TEXT,
    created_at      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id, nonce)
) PARTITION BY RANGE (epoch_id)`},
	},
	{
		ID:   5,
		Name: "devshard_signatures_parent",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_signatures (
    epoch_id  BIGINT NOT NULL,
    escrow_id TEXT   NOT NULL,
    nonce     BIGINT NOT NULL,
    slot_id   BIGINT NOT NULL,
    sig       BYTEA  NOT NULL,
    PRIMARY KEY (epoch_id, escrow_id, nonce, slot_id)
) PARTITION BY RANGE (epoch_id)`},
	},
	{
		ID:   6,
		Name: "devshard_snapshots_parent",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_snapshots (
    epoch_id   BIGINT NOT NULL,
    escrow_id  TEXT   NOT NULL,
    nonce      BIGINT NOT NULL,
    state_data BYTEA  NOT NULL,
    created_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id)
) PARTITION BY RANGE (epoch_id)`},
	},
	{
		ID:   7,
		Name: "devshard_sealed_inferences_parent",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_sealed_inferences (
    epoch_id             BIGINT  NOT NULL,
    escrow_id            TEXT    NOT NULL,
    inference_id         BIGINT  NOT NULL,
    sealed_nonce         BIGINT  NOT NULL,
    obs_present          BOOLEAN NOT NULL DEFAULT FALSE,
    sealed_status        INTEGER NOT NULL DEFAULT 0,
    sealed_executor_slot INTEGER NOT NULL DEFAULT 0,
    sealed_votes_valid   INTEGER NOT NULL DEFAULT 0,
    sealed_votes_invalid INTEGER NOT NULL DEFAULT 0,
    sealed_validated_by  BYTEA,
    sealed_model         TEXT    NOT NULL DEFAULT '',
    sealed_prompt_hash   BYTEA,
    sealed_response_hash BYTEA,
    sealed_input_length  BIGINT  NOT NULL DEFAULT 0,
    sealed_max_tokens    BIGINT  NOT NULL DEFAULT 0,
    sealed_input_tokens  BIGINT  NOT NULL DEFAULT 0,
    sealed_output_tokens BIGINT  NOT NULL DEFAULT 0,
    sealed_reserved_cost BIGINT  NOT NULL DEFAULT 0,
    sealed_actual_cost   BIGINT  NOT NULL DEFAULT 0,
    sealed_started_at    BIGINT  NOT NULL DEFAULT 0,
    sealed_confirmed_at  BIGINT  NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id, inference_id)
) PARTITION BY RANGE (epoch_id)`},
	},
	{
		ID:   8,
		Name: "devshard_slot_validation_obs_parent",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_slot_validation_obs (
    epoch_id               BIGINT NOT NULL,
    escrow_id              TEXT   NOT NULL,
    slot_id                INTEGER NOT NULL,
    required_validations   INTEGER NOT NULL DEFAULT 0,
    completed_validations  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id, slot_id)
) PARTITION BY RANGE (epoch_id)`},
	},
	{
		ID:   9,
		Name: "devshard_inference_validation_obs_parent",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS devshard_inference_validation_obs (
    epoch_id               BIGINT NOT NULL,
    escrow_id              TEXT NOT NULL,
    inference_id           BIGINT NOT NULL,
    slot_id                INTEGER NOT NULL,
    required_validations   INTEGER NOT NULL DEFAULT 0,
    completed_validations  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id, inference_id, slot_id)
) PARTITION BY RANGE (epoch_id)`,
			`CREATE TABLE IF NOT EXISTS devshard_sealed_validation_obs (
    epoch_id               BIGINT NOT NULL,
    escrow_id              TEXT NOT NULL,
    inference_id           BIGINT NOT NULL,
    slot_id                INTEGER NOT NULL,
    required_validations   INTEGER NOT NULL DEFAULT 0,
    completed_validations  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id, inference_id, slot_id)
) PARTITION BY RANGE (epoch_id)`},
	},
}

// MigratePostgres applies all pending devshard Postgres parent-table migrations.
func MigratePostgres(ctx context.Context, pool *pgxpool.Pool) error {
	if err := migrate.ApplyPG(ctx, pool, postgresMigrationSteps); err != nil {
		return fmt.Errorf("devshard postgres migrate: %w", err)
	}
	return nil
}

// PostgresMigrationSteps returns a copy of registered Postgres migration steps (for tests).
func PostgresMigrationSteps() []migrate.Step {
	out := make([]migrate.Step, len(postgresMigrationSteps))
	copy(out, postgresMigrationSteps)
	return out
}
