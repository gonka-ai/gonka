package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

const sqliteBootstrapSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
`

// ApplySQLite runs pending migrations on db in ascending step ID order.
// Each step runs in its own transaction (BEGIN … COMMIT).
//
// Devshard assumes one process owns a given SQLite file (meta sidecar or epoch
// pool). Per-step transactions serialize migrators inside that process only;
// two devshardd instances must not share the same store directory. WAL mode
// (set below) improves cross-connection behavior but does not replace that
// invariant — delete .pg-bound / use one process per data dir.
func ApplySQLite(ctx context.Context, db *sql.DB, steps []Step) error {
	if err := validateSteps(steps); err != nil {
		return err
	}
	if err := ensureSQLiteMigratePragmas(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, sqliteBootstrapSQL); err != nil {
		return fmt.Errorf("migrate: bootstrap schema_migrations: %w", err)
	}

	var maxApplied int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM schema_migrations`).Scan(&maxApplied); err != nil {
		return fmt.Errorf("migrate: read schema_migrations: %w", err)
	}

	for _, step := range steps {
		if step.ID <= maxApplied {
			continue
		}
		if err := applySQLiteStep(ctx, db, step); err != nil {
			return err
		}
		maxApplied = step.ID
	}
	return nil
}

// ensureSQLiteMigratePragmas matches devshard/storage/sqlite.go pool setup.
// Idempotent when callers already enabled WAL; required for safe concurrent
// readers while a migrator holds the writer lock on the same file.
func ensureSQLiteMigratePragmas(ctx context.Context, db *sql.DB) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("migrate: %s: %w", pragma, err)
		}
	}
	return nil
}

func applySQLiteStep(ctx context.Context, db *sql.DB, step Step) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin step %d (%s): %w", step.ID, step.Name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if step.SQLiteRun != nil {
		if err := step.SQLiteRun(ctx, tx); err != nil {
			return fmt.Errorf("migrate: step %d (%s): %w", step.ID, step.Name, err)
		}
	} else {
		if len(step.Statements) == 0 {
			return fmt.Errorf("migrate: step %d (%s): no statements", step.ID, step.Name)
		}
		for _, stmt := range step.Statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migrate: step %d (%s): %w", step.ID, step.Name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (id, name) VALUES (?, ?)`,
		step.ID, step.Name,
	); err != nil {
		return fmt.Errorf("migrate: record step %d (%s): %w", step.ID, step.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit step %d (%s): %w", step.ID, step.Name, err)
	}
	return nil
}

// AppliedSQLite returns the number of rows in schema_migrations.
func AppliedSQLite(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	return n, err
}
