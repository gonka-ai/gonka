package accounting

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"common/storage/pgtimeouts"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	accountingSQLiteImportMarker = "sqlite_import"
	// accountingBlobImportMarker guards the one-shot conversion of the legacy
	// one-blob-per-escrow layout into the additive row layout.
	accountingBlobImportMarker = "blob_to_rows"
)

type postgresBackend struct {
	pool      *pgxpool.Pool
	opTimeout time.Duration
	// writerID names this process's additive rows.
	writerID string

	// mu serializes writes and guards peers: Flush can be called from the
	// snapshot loop and from Recorder.Flush concurrently.
	mu sync.Mutex
	// peers is the contribution other writers had published for an escrow when
	// this process loaded it. A flush subtracts it from the in-memory total to
	// get its own share. Only escrows with peer rows are kept, so a
	// single-writer deployment carries no overhead.
	peers map[string]*escrowContribution
}

func openPostgresBackend(ctx context.Context) (*postgresBackend, error) {
	connectTimeout := pgtimeouts.ConnectTimeout()
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	cfg, err := pgxpool.ParseConfig("") // reads libpq env vars (PGHOST, PGPORT, ...)
	if err != nil {
		return nil, fmt.Errorf("parse accounting postgres config: %w", err)
	}
	pgtimeouts.ApplyConnConfig(cfg.ConnConfig)

	pool, err := pgxpool.NewWithConfig(connectCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect accounting postgres: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping accounting postgres: %w", err)
	}
	s := &postgresBackend{
		pool:      pool,
		opTimeout: pgtimeouts.OperationTimeout(),
		writerID:  accountingWriterID(),
		peers:     make(map[string]*escrowContribution),
	}
	if err := s.ensureSchema(connectCtx); err != nil {
		pool.Close()
		return nil, err
	}
	// The blob import reads and rewrites the whole ledger, so it gets the
	// import budget rather than the connect deadline.
	importCtx, cancelImport := context.WithTimeout(ctx, pgtimeouts.ImportTimeout())
	defer cancelImport()
	if err := s.importLegacyBlobLedger(importCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *postgresBackend) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if s == nil || s.opTimeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.opTimeout)
}

func (s *postgresBackend) Close() error {
	if s == nil || s.pool == nil {
		return nil
	}
	s.pool.Close()
	return nil
}

func (s *postgresBackend) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS accounting_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS accounting_migration (
			name TEXT PRIMARY KEY,
			completed_at TEXT NOT NULL
		)`,
		accountingRowSchema,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("init accounting postgres schema: %w", err)
		}
	}
	return nil
}

func (s *postgresBackend) Load(ctx context.Context, t *Tracker) error {
	if s == nil || s.pool == nil || t == nil {
		return nil
	}
	opCtx, cancel := s.opCtx(ctx)
	defer cancel()

	if err := s.loadWriterMeta(opCtx, t); err != nil {
		return err
	}

	ledger, err := readLedger(opCtx, s.pool, s.writerID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.peers = ledger.peers
	s.mu.Unlock()

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, blob := range ledger.blobs {
		if err := applyLoadedEscrow(t, *blob); err != nil {
			return err
		}
	}
	t.dirty = nil
	return nil
}

// loadWriterMeta restores this writer's error count and the ledger's freshness.
// writer_errors is per writer (it counts this process's failed persists), while
// updated_at is the newest write by any writer.
func (s *postgresBackend) loadWriterMeta(ctx context.Context, t *Tracker) error {
	rows, err := s.pool.Query(ctx, `SELECT writer_id, updated_at, writer_errors FROM accounting_writers`)
	if err != nil {
		return fmt.Errorf("load accounting writers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			writerID, updatedAt string
			writerErrors        int64
		)
		if err := rows.Scan(&writerID, &updatedAt, &writerErrors); err != nil {
			return err
		}
		if updated, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil && updated.After(t.updated) {
			t.updated = updated
		}
		if writerID == s.writerID && writerErrors > 0 {
			t.wrCount = uint64(writerErrors)
		}
	}
	return rows.Err()
}

func (s *postgresBackend) Save(ctx context.Context, snap storeSnapshot, dirtyIDs, deletedIDs []string) error {
	if s == nil || s.pool == nil {
		return nil
	}
	opCtx, cancel := s.opCtx(ctx)
	defer cancel()
	return s.save(opCtx, snap, dirtyIDs, deletedIDs)
}

// save runs the write under the caller's deadline. Bulk paths (the SQLite import)
// use it directly so they are not capped by the per-operation timeout.
//
// Only dirty escrows are written, and each one is written as this writer's own
// additive rows plus SQL-merged shared rows, so a concurrent gateway holding the
// same escrow never loses its counts and a retried flush is a no-op.
func (s *postgresBackend) save(opCtx context.Context, snap storeSnapshot, dirtyIDs, deletedIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(opCtx, s.writerID, snap, dirtyIDs, deletedIDs)
}

func (s *postgresBackend) saveLocked(
	opCtx context.Context,
	writerID string,
	snap storeSnapshot,
	dirtyIDs, deletedIDs []string,
) error {
	tx, err := s.pool.Begin(opCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(opCtx)

	schemaKey, schemaVal := schemaVersionMeta()
	if _, err := tx.Exec(opCtx, `
		INSERT INTO accounting_meta(key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		schemaKey, schemaVal,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(opCtx, `
		INSERT INTO accounting_writers(writer_id, updated_at, writer_errors)
		VALUES ($1, $2, $3)
		ON CONFLICT (writer_id) DO UPDATE SET
			updated_at = EXCLUDED.updated_at,
			writer_errors = EXCLUDED.writer_errors`,
		writerID, snap.UpdatedAt.Format(time.RFC3339Nano), int64(snap.WriterErrors),
	); err != nil {
		return err
	}

	byID := make(map[string]escrowBlob, len(snap.Escrows))
	for _, blob := range snap.Escrows {
		byID[blob.Meta.EscrowID] = blob
	}
	for _, id := range dirtyIDs {
		blob, ok := byID[id]
		if !ok {
			continue
		}
		mine := contributionFromBlob(blob).minus(s.peers[id])
		if err := writeEscrowRows(opCtx, tx, writerID, blob, mine); err != nil {
			return err
		}
	}
	if err := deleteEscrowRows(opCtx, tx, deletedIDs); err != nil {
		return err
	}
	if err := tx.Commit(opCtx); err != nil {
		return err
	}
	// A pruned escrow is gone from the ledger; a later escrow reusing the id
	// must not inherit a stale peer baseline.
	for _, id := range deletedIDs {
		delete(s.peers, id)
	}
	return nil
}

// importLegacyBlobLedger converts the pre-additive layout (one JSON blob per
// escrow) into rows. The imported numbers are attributed to a frozen legacy
// writer, so live writers see them as a peer contribution and add to them
// instead of restating them.
func (s *postgresBackend) importLegacyBlobLedger(ctx context.Context) error {
	exists, err := s.tableExists(ctx, "accounting_escrows")
	if err != nil {
		return err
	}
	migrated, err := s.hasMigrationMarker(ctx, accountingBlobImportMarker)
	if err != nil {
		return err
	}
	if migrated {
		if exists {
			s.warnLeftoverLegacyBlobs(ctx)
		}
		return nil
	}
	if !exists {
		return s.writeMigrationMarker(ctx, accountingBlobImportMarker)
	}

	rows, err := s.pool.Query(ctx, `SELECT payload FROM accounting_escrows`)
	if err != nil {
		return fmt.Errorf("read legacy accounting blobs: %w", err)
	}
	var blobs []escrowBlob
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		blob, err := decodeEscrowBlob(raw)
		if err != nil {
			rows.Close()
			return fmt.Errorf("decode legacy accounting blob: %w", err)
		}
		blobs = append(blobs, blob)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(blobs) == 0 {
		return s.writeMigrationMarker(ctx, accountingBlobImportMarker)
	}

	snap := storeSnapshot{UpdatedAt: time.Now().UTC(), Escrows: blobs}
	dirtyIDs := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		dirtyIDs = append(dirtyIDs, blob.Meta.EscrowID)
	}
	s.mu.Lock()
	err = s.saveLocked(ctx, accountingLegacyWriterID, snap, dirtyIDs, nil)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("import legacy accounting blobs: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM accounting_escrows`); err != nil {
		return fmt.Errorf("clear legacy accounting blobs: %w", err)
	}
	log.Printf("accounting store: converted %d legacy blob escrows to additive rows (writer=%s)", len(blobs), accountingLegacyWriterID)
	return s.writeMigrationMarker(ctx, accountingBlobImportMarker)
}

// warnLeftoverLegacyBlobs reports blobs written after the conversion ran, which
// only happens when an older build wrote to this database in between. Those
// writes are not imported (that would double count the rows they overlap), so
// the ledger is missing whatever only the older build saw.
func (s *postgresBackend) warnLeftoverLegacyBlobs(ctx context.Context) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM accounting_escrows`).Scan(&n); err != nil || n == 0 {
		return
	}
	log.Printf("accounting store: WARNING accounting_escrows holds %d blob rows written after the %s conversion (an older build wrote to this database); they are NOT imported, inspect and drop the table manually", n, accountingBlobImportMarker)
}

func (s *postgresBackend) tableExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return exists, nil
}

func migrateSQLiteAccountingToPostgres(ctx context.Context, sqlitePath string, dst *postgresBackend) error {
	if dst == nil || dst.pool == nil {
		return nil
	}
	if strings.TrimSpace(sqlitePath) == "" {
		return nil
	}
	if _, err := os.Stat(sqlitePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// The import loads the whole local ledger and writes every escrow in one
	// transaction, so it gets its own generous budget instead of opTimeout.
	opCtx, cancel := context.WithTimeout(ctx, pgtimeouts.ImportTimeout())
	defer cancel()

	migrated, err := dst.hasMigrationMarker(opCtx, accountingSQLiteImportMarker)
	if err != nil {
		return err
	}
	if migrated {
		return nil
	}

	var n int
	if err := dst.pool.QueryRow(opCtx, `SELECT COUNT(*) FROM accounting_escrow_state`).Scan(&n); err != nil {
		return fmt.Errorf("check destination accounting escrows: %w", err)
	}
	if n > 0 {
		return dst.writeMigrationMarker(opCtx, accountingSQLiteImportMarker)
	}

	src, err := openSQLiteBackend(sqlitePath)
	if err != nil {
		return fmt.Errorf("open sqlite accounting for migrate: %w", err)
	}
	defer src.Close()

	tmp := &Tracker{escrows: make(map[string]*escrowState), now: time.Now}
	if err := src.Load(opCtx, tmp); err != nil {
		return fmt.Errorf("load sqlite accounting for migrate: %w", err)
	}
	if len(tmp.escrows) == 0 {
		return dst.writeMigrationMarker(opCtx, accountingSQLiteImportMarker)
	}

	// One-shot import: treat every loaded escrow as dirty.
	tmp.mu.Lock()
	tmp.dirty = make(map[string]struct{}, len(tmp.escrows))
	for id := range tmp.escrows {
		tmp.dirty[id] = struct{}{}
	}
	tmp.mu.Unlock()

	snap, dirtyIDs, deletedIDs := tmp.takePersistSnapshot(0)
	if err := dst.save(opCtx, snap, dirtyIDs, deletedIDs); err != nil {
		return fmt.Errorf("import sqlite accounting into postgres: %w", err)
	}
	return dst.writeMigrationMarker(opCtx, accountingSQLiteImportMarker)
}

func (s *postgresBackend) hasMigrationMarker(ctx context.Context, name string) (bool, error) {
	var completedAt string
	err := s.pool.QueryRow(ctx, `SELECT completed_at FROM accounting_migration WHERE name = $1`, name).Scan(&completedAt)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *postgresBackend) writeMigrationMarker(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO accounting_migration (name, completed_at)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET completed_at = EXCLUDED.completed_at`,
		name, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}
