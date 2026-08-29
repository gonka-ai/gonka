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

// ApplyPG runs pending migrations on pool in ascending step ID order.
// Each step runs in its own transaction.
func ApplyPG(ctx context.Context, pool *pgxpool.Pool, steps []Step) error {
	if err := validateSteps(steps); err != nil {
		return err
	}
	if err := bootstrapPG(ctx, pool); err != nil {
		return fmt.Errorf("migrate: bootstrap schema_migrations: %w", err)
	}

	for _, step := range steps {
		if err := applyPGStep(ctx, pool, step); err != nil {
			return err
		}
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

func beginLockedPGMigrationTx(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// Application queries use short server-side timeouts. A migration waiting
	// behind another migrator is normal coordination, so let the caller's context
	// bound this transaction instead of failing the process on lock_timeout.
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

func applyPGStep(ctx context.Context, pool *pgxpool.Pool, step Step) error {
	tx, err := beginLockedPGMigrationTx(ctx, pool)
	if err != nil {
		return fmt.Errorf("migrate: begin step %d (%s): %w", step.ID, step.Name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var maxApplied int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM schema_migrations`).Scan(&maxApplied); err != nil {
		return fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	if step.ID <= maxApplied {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit skipped step %d (%s): %w", step.ID, step.Name, err)
		}
		return nil
	}

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
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate: commit step %d (%s): %w", step.ID, step.Name, err)
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
