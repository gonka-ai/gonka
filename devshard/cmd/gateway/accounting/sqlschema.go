package accounting

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// The tables mirror the in-memory shape; a write replaces the rows of the escrows it carries in one transaction.
const schema = `
CREATE TABLE IF NOT EXISTS accounting_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounting_escrows (
	escrow_id      TEXT PRIMARY KEY,
	model          TEXT    NOT NULL,
	creation_epoch INTEGER NOT NULL,
	latest_nonce   INTEGER NOT NULL,
	fee_per_nonce  INTEGER NOT NULL,
	retired        INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS accounting_slots (
	escrow_id         TEXT    NOT NULL,
	slot_id           INTEGER NOT NULL,
	validator_address TEXT    NOT NULL,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_host_stats (
	escrow_id             TEXT    NOT NULL,
	slot_id               INTEGER NOT NULL,
	missed                INTEGER NOT NULL,
	invalid               INTEGER NOT NULL,
	cost                  INTEGER NOT NULL,
	required_validations  INTEGER NOT NULL,
	completed_validations INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_slot_activity (
	escrow_id        TEXT    NOT NULL,
	slot_id          INTEGER NOT NULL,
	challenged       INTEGER NOT NULL,
	validations      INTEGER NOT NULL,
	timeouts_applied INTEGER NOT NULL,
	rejected         INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_service_nonces (
	escrow_id TEXT    NOT NULL,
	slot_id   INTEGER NOT NULL,
	purpose   TEXT    NOT NULL,
	count     INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, slot_id, purpose)
);
CREATE TABLE IF NOT EXISTS accounting_money (
	escrow_id        TEXT    NOT NULL,
	slot_id          INTEGER NOT NULL,
	reserved_cost    INTEGER NOT NULL,
	actual_cost      INTEGER NOT NULL,
	refunded_cost    INTEGER NOT NULL,
	estimated_input  INTEGER NOT NULL,
	estimated_error  INTEGER NOT NULL,
	max_tokens       INTEGER NOT NULL,
	input_tokens     INTEGER NOT NULL,
	counted_nonces   INTEGER NOT NULL,
	output_tokens    INTEGER NOT NULL,
	tokenized_input  INTEGER,
	tokenized_error  INTEGER,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_first_content (
	escrow_id          TEXT    NOT NULL,
	slot_id            INTEGER NOT NULL,
	total_milliseconds INTEGER NOT NULL,
	attempts           INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_first_content_distribution (
	escrow_id TEXT    NOT NULL,
	bucket    INTEGER NOT NULL,
	attempts  INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, bucket)
);
CREATE TABLE IF NOT EXISTS accounting_counters (
	escrow_id      TEXT    NOT NULL,
	slot_id        INTEGER NOT NULL,
	disposition    TEXT    NOT NULL,
	ghost_reason   TEXT    NOT NULL,
	timeout_kind   TEXT    NOT NULL,
	timeout_action TEXT    NOT NULL,
	timeout_reason TEXT    NOT NULL,
	terminal       TEXT    NOT NULL,
	phase          TEXT    NOT NULL,
	slow_receipt     INTEGER NOT NULL,
	slow_chunk       INTEGER NOT NULL,
	clock_drifted    INTEGER NOT NULL,
	slow_decode      INTEGER NOT NULL,
	logprobs_decoded INTEGER NOT NULL,
	count            INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, slot_id, disposition, ghost_reason, timeout_kind, timeout_action, timeout_reason,
	             terminal, phase, slow_receipt, slow_chunk, clock_drifted, slow_decode, logprobs_decoded)
);
CREATE TABLE IF NOT EXISTS accounting_nonces (
	escrow_id      TEXT    NOT NULL,
	nonce          INTEGER NOT NULL,
	sent           INTEGER NOT NULL,
	acknowledged   INTEGER NOT NULL,
	usage          TEXT    NOT NULL,
	timeout_kind   TEXT    NOT NULL,
	timeout_action TEXT    NOT NULL,
	timeout_reason TEXT    NOT NULL,
	terminal       TEXT    NOT NULL,
	phase          TEXT    NOT NULL,
	slow_receipt     INTEGER NOT NULL,
	slow_chunk       INTEGER NOT NULL,
	clock_drifted    INTEGER NOT NULL,
	slow_decode      INTEGER NOT NULL,
	logprobs_decoded INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, nonce)
);
CREATE TABLE IF NOT EXISTS accounting_costs (
	escrow_id    TEXT    NOT NULL,
	nonce        INTEGER NOT NULL,
	reserved     INTEGER NOT NULL,
	actual       INTEGER NOT NULL,
	input_length INTEGER NOT NULL,
	max_tokens   INTEGER NOT NULL,
	input        INTEGER NOT NULL,
	output       INTEGER NOT NULL,
	status       INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, nonce)
);
CREATE TABLE IF NOT EXISTS accounting_input_counts (
	escrow_id TEXT    NOT NULL,
	nonce     INTEGER NOT NULL,
	tokens    INTEGER NOT NULL,
	source    TEXT    NOT NULL,
	PRIMARY KEY (escrow_id, nonce)
);
`

var (
	addedColumns = []struct{ table, column, definition, storedRowsValue string }{
		{"accounting_money", "tokenized_input", "INTEGER", "0"},
		{"accounting_money", "tokenized_error", "INTEGER", "0"},
	}
	escrowTables = []string{
		"accounting_input_counts",
		"accounting_costs",
		"accounting_nonces",
		"accounting_counters",
		"accounting_host_stats",
		"accounting_slot_activity",
		"accounting_service_nonces",
		"accounting_money",
		firstContentTable,
		firstContentDistributionTable,
		"accounting_slots",
		"accounting_escrows",
	}
	clearedTables      = append(slices.Clone(escrowTables), "accounting_meta")
	firstContentTables = []string{firstContentTable, firstContentDistributionTable}
)

const (
	firstContentTable                     = "accounting_first_content"
	firstContentDistributionTable         = "accounting_first_content_distribution"
	metaSchemaVersion                     = "schema_version"
	metaUpdatedAt                         = "updated_at"
	metaFirstContentDistributionWrittenAt = "first_content_distribution_written_at"
	selectMeta                            = `SELECT value FROM accounting_meta WHERE key = ?`
)

func discardOutdatedSchema(db *sql.DB) error {
	if currentSchema(db) {
		return nil
	}
	for _, table := range clearedTables {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			return fmt.Errorf("dropping %s of an accounting store this build cannot write: %w", table, err)
		}
	}
	return nil
}

// An unreadable version says nothing about the tables beside it, so they are discarded rather than trusted.
func currentSchema(db *sql.DB) bool {
	var stored string
	if err := db.QueryRow(selectMeta, metaSchemaVersion).Scan(&stored); err != nil {
		return false
	}
	if stored != strconv.Itoa(SchemaVersion) {
		return false
	}
	for _, probe := range []string{
		`SELECT terminal, phase, slow_receipt, slow_chunk, clock_drifted, slow_decode, logprobs_decoded FROM accounting_counters LIMIT 0`,
		`SELECT terminal, phase, slow_receipt, slow_chunk, clock_drifted, slow_decode, logprobs_decoded FROM accounting_nonces LIMIT 0`,
	} {
		if _, err := db.Exec(probe); err != nil {
			return false
		}
	}
	return true
}

func addMissingColumns(db *sql.DB) error {
	for _, added := range addedColumns {
		var present bool
		if err := db.QueryRow(`SELECT COUNT(*) > 0 FROM pragma_table_info(?) WHERE name = ?`, added.table, added.column).Scan(&present); err != nil {
			return fmt.Errorf("inspecting column %s.%s: %w", added.table, added.column, err)
		}
		if present {
			continue
		}
		transaction, err := db.Begin()
		if err != nil {
			return fmt.Errorf("beginning to add column %s.%s: %w", added.table, added.column, err)
		}
		if _, err := transaction.Exec(`ALTER TABLE ` + added.table + ` ADD COLUMN ` + added.column + ` ` + added.definition); err != nil {
			return errors.Join(fmt.Errorf("adding column %s.%s: %w", added.table, added.column, err), transaction.Rollback())
		}
		if _, err := transaction.Exec(`UPDATE ` + added.table + ` SET ` + added.column + ` = ` + added.storedRowsValue); err != nil {
			return errors.Join(fmt.Errorf("filling column %s.%s of the stored rows: %w", added.table, added.column, err), transaction.Rollback())
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("committing column %s.%s: %w", added.table, added.column, err)
		}
	}
	return nil
}

func discardUntrustedFirstContent(db *sql.DB) error {
	trusted := firstContentWrittenWithLastSave(db)
	for _, table := range firstContentTables {
		statement := `DELETE FROM ` + table
		if trusted {
			statement += ` WHERE escrow_id NOT IN (SELECT escrow_id FROM accounting_escrows)`
		}
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("discarding stale rows of %s: %w", table, err)
		}
	}
	return nil
}

func firstContentWrittenWithLastSave(db *sql.DB) bool {
	var updatedAt, distributionWrittenAt string
	if db.QueryRow(selectMeta, metaUpdatedAt).Scan(&updatedAt) != nil ||
		db.QueryRow(selectMeta, metaFirstContentDistributionWrittenAt).Scan(&distributionWrittenAt) != nil {
		return false
	}
	return updatedAt == distributionWrittenAt
}
