package migrate

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgMigrationLockNamespace is "GONK" encoded as an int32. The second advisory
// lock key is the database name hash, so independent databases do not block
// each other while every devshard migrator for one database is serialized.
const pgMigrationLockNamespace int32 = 0x474f4e4b

const pgBootstrapSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    id INT PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TIMESTAMP DEFAULT NOW()
);
`

// ApplyPG runs pending migrations on one connection under one advisory lock.
// The transaction makes a failed migration batch retryable as a whole and
// avoids one lock round trip for every already-applied step at process boot.
func ApplyPG(ctx context.Context, pool *pgxpool.Pool, steps []Step) error {
	if err := validateSteps(steps); err != nil {
		return err
	}
	// Keep the migration ledger even when a later application step rolls back.
	// This is a separate short locked transaction; all real steps below still
	// share one lock acquisition and one transaction.
	if err := bootstrapPG(ctx, pool); err != nil {
		return fmt.Errorf("migrate: bootstrap schema_migrations: %w", err)
	}
	tx, err := beginLockedPGMigrationTx(ctx, pool)
	if err != nil {
		return fmt.Errorf("migrate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	applied, err := appliedPGStepIDs(ctx, tx)
	if err != nil {
		return err
	}

	for _, step := range steps {
		if _, ok := applied[step.ID]; ok {
			continue
		}
		if err := applyPGStep(ctx, tx, step); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate: commit: %w", err)
	}
	return nil
}

func bootstrapPG(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := beginLockedPGMigrationTx(ctx, pool)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, pgBootstrapSQL); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appliedPGStepIDs(ctx context.Context, tx pgx.Tx) (map[int]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]struct{})
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("migrate: scan schema_migrations: %w", err)
		}
		applied[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	return applied, nil
}

func beginLockedPGMigrationTx(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// Application queries use short server-side timeouts. A migration waiting
	// behind another migrator is normal coordination, so let the dedicated
	// migration context bound this transaction.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = 0`); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, fmt.Errorf("disable migration lock timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, fmt.Errorf("disable migration statement timeout: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock($1, hashtext(current_database()))`,
		pgMigrationLockNamespace,
	); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}
	return tx, nil
}

func applyPGStep(ctx context.Context, tx pgx.Tx, step Step) error {
	if len(step.Statements) == 0 {
		return fmt.Errorf("migrate: step %d (%s): no statements", step.ID, step.Name)
	}
	for _, stmt := range step.Statements {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: step %d (%s): %w", step.ID, step.Name, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (id, name) VALUES ($1, $2)`,
		step.ID, step.Name,
	); err != nil {
		return fmt.Errorf("migrate: record step %d (%s): %w", step.ID, step.Name, err)
	}
	return nil
}

// AppliedPG returns the number of rows in schema_migrations.
func AppliedPG(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n)
	return n, err
}

// MaxAppliedPG returns the highest applied migration ID, or 0 if none.
func MaxAppliedPG(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var maxID int
	err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM schema_migrations`).Scan(&maxID)
	return maxID, err
}

// TableExistsPG reports whether a table exists in the public schema.
func TableExistsPG(ctx context.Context, pool *pgxpool.Pool, table string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = 'public' AND table_name = $1
)`, table).Scan(&exists)
	return exists, err
}
