package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"common/storage/mode"
	"common/storage/pgtimeouts"

	"github.com/jackc/pgx/v5"
)

const (
	perfSQLiteImportMarker = "perf_sqlite_import"
	perfImportPageSize     = 2000
)

// testHookPerfImportPageCopied runs between import pages in tests; it is nil in production.
var testHookPerfImportPageCopied func(tableName string)

// perfImportTable copies one perf.db table: selectPage reads the key column first and then insertRow's values in order.
type perfImportTable struct {
	name                string
	selectPage          string
	insertRow           string
	afterKey            int64
	selectSourceMaxID   string
	selectExistingMinID string
}

// NewPerfStore picks the backend like NewGatewayStore: sqlite keeps perf.db; hybrid and postgres import it once and fail closed.
func NewPerfStore(ctx context.Context, baseStorageDir string) (PerfStore, error) {
	storageMode, err := mode.Resolve()
	if err != nil {
		return nil, err
	}
	sqlitePath := filepath.Join(baseStorageDir, "perf.db")
	pgHost := strings.TrimSpace(os.Getenv("PGHOST"))

	if !storageMode.RequiresPGHOST() {
		if pgHost != "" {
			log.Printf("perf store: %s=%s ignores PGHOST (%s)", mode.EnvStorageMode, storageMode, pgHost)
		}
		store, err := newSQLitePerfStore(sqlitePath)
		if err != nil {
			return nil, err
		}
		return store, nil
	}
	if pgHost == "" {
		return nil, fmt.Errorf("perf store: mode %s requires PGHOST", storageMode)
	}

	store, err := newPostgresPerfStore(ctx, sqlitePath)
	if err != nil {
		return nil, fmt.Errorf("perf store: postgres required (mode=%s, host=%s): %w", storageMode, pgHost, err)
	}
	if err := importPerfSQLite(ctx, sqlitePath, store); err != nil {
		_ = store.Close()
		return nil, err
	}
	log.Printf("perf store: using postgres only (mode=%s, host=%s)", storageMode, pgHost)
	return store, nil
}

// importPerfSQLite copies each perf.db file once; history ordered by id lands below every row already there, live writes included.
func importPerfSQLite(ctx context.Context, sqlitePath string, store *postgresPerfStore) error {
	if _, err := os.Stat(sqlitePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("perf store: stat %s: %w", sqlitePath, err)
	}
	importCtx, cancel := context.WithTimeout(ctx, pgtimeouts.ImportTimeout())
	defer cancel()

	source, err := newSQLitePerfStore(sqlitePath)
	if err != nil {
		return fmt.Errorf("perf store: open %s for import: %w", sqlitePath, err)
	}
	defer func() { _ = source.Close() }()
	sourceID, err := source.importSourceID(importCtx)
	if err != nil {
		return fmt.Errorf("perf store: identify %s for import: %w", sqlitePath, err)
	}
	marker := perfSQLiteImportMarker + ":" + sourceID
	if err := ensureGatewayMigrationTable(importCtx, store.pool); err != nil {
		return fmt.Errorf("perf store: ensure migration table: %w", err)
	}

	tx, err := store.pool.Begin(importCtx)
	if err != nil {
		return fmt.Errorf("perf store: begin import: %w", err)
	}
	defer func() { _ = tx.Rollback(importCtx) }()
	for _, statement := range []string{`SET LOCAL lock_timeout = 0`, `SET LOCAL statement_timeout = 0`} {
		if _, err := tx.Exec(importCtx, statement); err != nil {
			return fmt.Errorf("perf store: %s: %w", statement, err)
		}
	}
	if _, err := tx.Exec(importCtx, `SELECT pg_advisory_xact_lock(hashtext($1))`, perfSQLiteImportMarker); err != nil {
		return fmt.Errorf("perf store: lock import of %s: %w", sqlitePath, err)
	}
	var marked int
	if err := tx.QueryRow(importCtx, `SELECT count(*) FROM gateway_migration WHERE name = $1`, marker).Scan(&marked); err != nil {
		return fmt.Errorf("perf store: read import marker: %w", err)
	}
	if marked > 0 {
		return nil
	}
	sampleBoundary, _, err := findHostSampleBoundary(importCtx, source, time.Now(), func() bool { return true })
	if err != nil {
		return fmt.Errorf("perf store: find sample boundary: %w", err)
	}
	logBoundary, err := source.requestLogRetentionBoundary(importCtx)
	if err != nil {
		return fmt.Errorf("perf store: find request log boundary: %w", err)
	}
	_, isSampleWalkFull, err := findHostSampleBoundary(importCtx, store, time.Now(), func() bool { return true })
	if err != nil {
		return fmt.Errorf("perf store: find served sample boundary: %w", err)
	}
	var retainedRequests int
	if err := tx.QueryRow(importCtx, `SELECT count(*) FROM (SELECT 1 FROM perf_request_log LIMIT $1) AS retained`, requestLogSize).Scan(&retainedRequests); err != nil {
		return fmt.Errorf("perf store: count served request log: %w", err)
	}
	var summary []string
	for _, table := range perfImportTables(sampleBoundary, logBoundary, !isSampleWalkFull, retainedRequests < requestLogSize) {
		copied, err := copyPerfImportTable(importCtx, source, tx, table)
		if err != nil {
			return fmt.Errorf("perf store: import %s: %w", table.name, err)
		}
		summary = append(summary, fmt.Sprintf("%s=%d", table.name, copied))
	}
	if err := writeGatewayMigrationMarker(importCtx, tx, marker, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("perf store: write import marker: %w", err)
	}
	if err := tx.Commit(importCtx); err != nil {
		return fmt.Errorf("perf store: commit import: %w", err)
	}
	log.Printf("perf store: imported %s from %s", strings.Join(summary, " "), sqlitePath)
	return nil
}

// perfImportTables skips history a served database would never read: imported rows land below its walk's end or its log.
func perfImportTables(sampleBoundary, logBoundary int64, copiesSamples, copiesRequestLog bool) []perfImportTable {
	var tables []perfImportTable
	if copiesSamples {
		tables = append(tables, perfImportTable{
			name: "perf_host_samples",
			selectPage: `SELECT id, host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow, source_sample_id
				FROM perf_host_samples WHERE id > ? ORDER BY id LIMIT ?`,
			insertRow: `INSERT INTO perf_host_samples (id, host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow, source_sample_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT (source_escrow, source_sample_id) WHERE source_sample_id IS NOT NULL DO NOTHING`,
			afterKey:            sampleBoundary,
			selectSourceMaxID:   `SELECT COALESCE(MAX(id), 0) FROM perf_host_samples`,
			selectExistingMinID: `SELECT LEAST(COALESCE(MIN(id), $1), $1) FROM perf_host_samples`,
		})
	}
	if copiesRequestLog {
		tables = append(tables, perfImportTable{
			name: "perf_request_log",
			selectPage: `SELECT id, timestamp, model, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json
				FROM perf_request_log WHERE id > ? ORDER BY id LIMIT ?`,
			insertRow: `INSERT INTO perf_request_log (id, timestamp, model, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			afterKey:            logBoundary,
			selectSourceMaxID:   `SELECT COALESCE(MAX(id), 0) FROM perf_request_log`,
			selectExistingMinID: `SELECT LEAST(COALESCE(MIN(id), $1), $1) FROM perf_request_log`,
		})
	}
	return append(tables, []perfImportTable{
		{
			name: "request_accounting",
			selectPage: `SELECT rowid, request_id, escrow_id, model, started_at, completed_at, outcome, decision, winner_nonce
				FROM request_accounting WHERE rowid > ? ORDER BY rowid LIMIT ?`,
			insertRow: `INSERT INTO request_accounting (request_id, escrow_id, model, started_at, completed_at, outcome, decision, winner_nonce)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
		},
		{
			name: "request_accounting_attempts",
			selectPage: `SELECT rowid, request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at
				FROM request_accounting_attempts WHERE rowid > ? ORDER BY rowid LIMIT ?`,
			insertRow: `INSERT INTO request_accounting_attempts (request_id, escrow_id, nonce, host_idx, participant_key, probe, winner, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
		},
		{
			name: "request_accounting_aliases",
			selectPage: `SELECT rowid, request_id, escrow_id, source_request_id, source_escrow_id, reason, created_at
				FROM request_accounting_aliases WHERE rowid > ? ORDER BY rowid LIMIT ?`,
			insertRow: `INSERT INTO request_accounting_aliases (request_id, escrow_id, source_request_id, source_escrow_id, reason, created_at)
				VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
		},
	}...)
}

// copyPerfImportTable streams one table page by page, so the import holds one page of rows at a time.
func copyPerfImportTable(ctx context.Context, source *sqlitePerfStore, tx pgx.Tx, table perfImportTable) (int64, error) {
	idOffset, err := importIDOffset(ctx, source, tx, table)
	if err != nil {
		return 0, err
	}
	var copied int64
	afterKey := table.afterKey
	for {
		page, lastKey, err := source.readImportPage(ctx, table.selectPage, afterKey)
		if err != nil {
			return copied, err
		}
		if len(page) == 0 {
			return copied, nil
		}
		batch := &pgx.Batch{}
		for _, values := range page {
			if !table.isOrderedByID() {
				values = values[1:]
			} else {
				values[0] = values[0].(int64) + idOffset
			}
			batch.Queue(table.insertRow, values...)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return copied, err
		}
		copied += int64(len(page))
		if testHookPerfImportPageCopied != nil {
			testHookPerfImportPageCopied(table.name)
		}
		if len(page) < perfImportPageSize {
			return copied, nil
		}
		afterKey = lastKey
	}
}

func (table perfImportTable) isOrderedByID() bool {
	return table.selectExistingMinID != ""
}

// importIDOffset places the file's rows just below the lowest id the table holds, keeping their order.
func importIDOffset(ctx context.Context, source *sqlitePerfStore, tx pgx.Tx, table perfImportTable) (int64, error) {
	if !table.isOrderedByID() {
		return 0, nil
	}
	var sourceMaxID, existingMinID int64
	if err := source.db.QueryRowContext(ctx, table.selectSourceMaxID).Scan(&sourceMaxID); err != nil {
		return 0, fmt.Errorf("read source max id: %w", err)
	}
	if err := tx.QueryRow(ctx, table.selectExistingMinID, perfImportedIDCeiling).Scan(&existingMinID); err != nil {
		return 0, fmt.Errorf("read min id: %w", err)
	}
	offset := existingMinID - 1 - sourceMaxID
	if lowestImportedID := table.afterKey + 1 + offset; lowestImportedID < 1 {
		return 0, fmt.Errorf("no id room below %d for an id span of %d", existingMinID, sourceMaxID-table.afterKey)
	}
	return offset, nil
}
