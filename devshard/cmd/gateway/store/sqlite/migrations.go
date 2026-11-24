package sqlite

import (
	"database/sql"
	"fmt"
	"regexp"
)

const createAPIKeysTable = `CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		key TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		revoked_at TEXT NOT NULL DEFAULT ''
	);`

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS config_overrides (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		overrides_json TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE TABLE IF NOT EXISTS devshards (
		escrow_id TEXT PRIMARY KEY,
		private_key_env TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL,
		active INTEGER NOT NULL DEFAULT 1,
		rotation_role TEXT NOT NULL DEFAULT 'regular',
		rotation_epoch INTEGER NOT NULL DEFAULT 0,
		settlement_pending INTEGER NOT NULL DEFAULT 0,
		settle_tx_hash TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);`,
	`CREATE TABLE IF NOT EXISTS escrow_rotation_commitments (
		tx_hash TEXT PRIMARY KEY,
		model TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT '',
		private_key_env TEXT NOT NULL DEFAULT '',
		epoch INTEGER NOT NULL DEFAULT 0,
		block_height INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS gateway_rotation_status (
		model TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT '',
		stage TEXT NOT NULL DEFAULT '',
		epoch INTEGER NOT NULL DEFAULT 0,
		completed INTEGER NOT NULL DEFAULT 0,
		create_error TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY (model, role)
	);`,
	`CREATE TABLE IF NOT EXISTS request_accounting (
		request_id TEXT PRIMARY KEY,
		escrow_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT '',
		decision TEXT NOT NULL DEFAULT '',
		stream INTEGER NOT NULL DEFAULT 0,
		winner_nonce INTEGER NOT NULL DEFAULT 0,
		winner_participant TEXT NOT NULL DEFAULT '',
		winner_host TEXT NOT NULL DEFAULT '',
		winner_host_idx INTEGER NOT NULL DEFAULT 0,
		attempts INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		winner_output_tokens INTEGER NOT NULL DEFAULT 0,
		total_output_tokens INTEGER NOT NULL DEFAULT 0,
		escrow_missing INTEGER NOT NULL DEFAULT 0,
		balance_exhausted INTEGER NOT NULL DEFAULT 0,
		started_at TEXT NOT NULL DEFAULT '',
		completed_at TEXT NOT NULL DEFAULT '',
		first_token_ms INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		recorded_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS request_accounting_recorded_at ON request_accounting (recorded_at);`,
	`CREATE TABLE IF NOT EXISTS suspicious_hosts (
		participant_key TEXT PRIMARY KEY,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);`,
	`ALTER TABLE devshards ADD COLUMN route_prefix TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE devshards ADD COLUMN settle_tx_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE request_accounting ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE devshards ADD COLUMN chain_epoch INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE devshards ADD COLUMN amount INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE devshards ADD COLUMN gone_from_chain INTEGER NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS model_filters (
		model TEXT PRIMARY KEY,
		preset TEXT NOT NULL,
		filters_json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`ALTER TABLE request_accounting ADD COLUMN input_tokens_source TEXT NOT NULL DEFAULT 'bytes_estimate'`,
	`CREATE TABLE IF NOT EXISTS owed_refusal_votes (
		escrow_id TEXT NOT NULL,
		nonce INTEGER NOT NULL,
		started_at TEXT NOT NULL,
		request_id TEXT NOT NULL DEFAULT '',
		participant TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		prompt BLOB NOT NULL,
		PRIMARY KEY (escrow_id, nonce)
	);`,
	createAPIKeysTable,
	`CREATE TABLE IF NOT EXISTS inference_completions (
		escrow_id TEXT NOT NULL,
		nonce INTEGER NOT NULL,
		entry_json BLOB NOT NULL,
		PRIMARY KEY (escrow_id, nonce)
	);`,
	createAPIKeysTable,
}

var addColumnMigration = regexp.MustCompile(`^ALTER TABLE (\w+) ADD COLUMN (\w+) `)

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}
	var currentVersion int
	row := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&currentVersion); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	for index := currentVersion; index < len(migrations); index++ {
		transaction, err := db.Begin()
		if err != nil {
			return fmt.Errorf("beginning migration %d: %w", index+1, err)
		}
		if err := applyMigration(transaction, migrations[index]); err != nil {
			transaction.Rollback()
			return fmt.Errorf("applying migration %d: %w", index+1, err)
		}
		if _, err := transaction.Exec(`INSERT INTO schema_version (version) VALUES (?)`, index+1); err != nil {
			transaction.Rollback()
			return fmt.Errorf("recording migration %d: %w", index+1, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", index+1, err)
		}
	}
	for index, statement := range migrations[:min(currentVersion, len(migrations))] {
		if !addColumnMigration.MatchString(statement) {
			continue
		}
		if err := applyMigration(db, statement); err != nil {
			return fmt.Errorf("re-applying migration %d: %w", index+1, err)
		}
	}
	return nil
}

type statementExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func applyMigration(executor statementExecutor, statement string) error {
	if match := addColumnMigration.FindStringSubmatch(statement); match != nil {
		var present bool
		if err := executor.QueryRow(`SELECT COUNT(*) > 0 FROM pragma_table_info(?) WHERE name = ?`, match[1], match[2]).Scan(&present); err != nil {
			return fmt.Errorf("inspecting column %s.%s: %w", match[1], match[2], err)
		}
		if present {
			return nil
		}
	}
	_, err := executor.Exec(statement)
	return err
}
