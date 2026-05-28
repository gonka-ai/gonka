package storage

import (
	"context"
	"database/sql"
	"fmt"

	"devshard/storage/migrate"
)

var sqliteMetaMigrationSteps = []migrate.Step{
	{
		ID:   1,
		Name: "escrow_epoch",
		Statements: []string{`
CREATE TABLE IF NOT EXISTS escrow_epoch (
    escrow_id TEXT PRIMARY KEY,
    epoch_id  INTEGER NOT NULL
)`},
	},
	{
		ID:         2,
		Name:       "escrow_epoch_by_epoch",
		Statements: []string{`CREATE INDEX IF NOT EXISTS escrow_epoch_by_epoch ON escrow_epoch(epoch_id)`},
	},
	{
		ID:         3,
		Name:       "noop",
		Statements: []string{`SELECT 1`},
	},
}

// MigrateMeta applies schema migrations for _meta.db.
func MigrateMeta(ctx context.Context, db *sql.DB) error {
	if err := migrate.ApplySQLite(ctx, db, sqliteMetaMigrationSteps); err != nil {
		return fmt.Errorf("devshard sqlite meta migrate: %w", err)
	}
	return nil
}

// SQLiteMetaMigrationSteps returns a copy of meta migration steps (for tests).
func SQLiteMetaMigrationSteps() []migrate.Step {
	out := make([]migrate.Step, len(sqliteMetaMigrationSteps))
	copy(out, sqliteMetaMigrationSteps)
	return out
}
