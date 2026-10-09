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

// perfImportTable copies one perf.db table: selectPage reads the key column first and then insertRow's values in order.
type perfImportTable struct {
	name       string
	selectPage string
	insertRow  string
	afterKey   int64
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

// importPerfSQLite copies each perf.db file once; samples and the request log only into empty tables, since their order is their id.
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
	var hasSamples, hasRequestLog bool
	if err := tx.QueryRow(importCtx, `SELECT EXISTS (SELECT 1 FROM perf_host_samples), EXISTS (SELECT 1 FROM perf_request_log)`).Scan(&hasSamples, &hasRequestLog); err != nil {
		return fmt.Errorf("perf store: check served history: %w", err)
	}
	var summary []string
	for _, table := range perfImportTables(sampleBoundary, logBoundary, !hasSamples, !hasRequestLog) {
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

// perfImportTables lists the copy of each table; ids are left to Postgres so its identity sequences stay ahead of them.
func perfImportTables(sampleBoundary, logBoundary int64, isSampleTableEmpty, isRequestLogEmpty bool) []perfImportTable {
	var tables []perfImportTable
	if isSampleTableEmpty {
		tables = append(tables, perfImportTable{
			name: "perf_host_samples",
			selectPage: `SELECT id, host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow, source_sample_id
				FROM perf_host_samples WHERE id > ? ORDER BY id LIMIT ?`,
			insertRow: `INSERT INTO perf_host_samples (host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow, source_sample_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
			afterKey: sampleBoundary,
		})
	}
	if isRequestLogEmpty {
		tables = append(tables, perfImportTable{
			name: "perf_request_log",
			selectPage: `SELECT id, timestamp, model, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json
				FROM perf_request_log WHERE id > ? ORDER BY id LIMIT ?`,
			insertRow: `INSERT INTO perf_request_log (timestamp, model, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			afterKey: logBoundary,
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
			batch.Queue(table.insertRow, values...)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return copied, err
		}
		copied += int64(len(page))
		if len(page) < perfImportPageSize {
			return copied, nil
		}
		afterKey = lastKey
	}
}
