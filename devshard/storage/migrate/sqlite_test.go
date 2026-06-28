package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/storage/migrate"

	_ "modernc.org/sqlite"
)

func openSQLiteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func fixtureSteps() []migrate.Step {
	return []migrate.Step{
		{
			ID:         1,
			Name:       "create_widget",
			Statements: []string{`CREATE TABLE IF NOT EXISTS widget (id INTEGER PRIMARY KEY, label TEXT NOT NULL)`},
		},
		{
			ID:         2,
			Name:       "add_widget_color",
			Statements: []string{`ALTER TABLE widget ADD COLUMN color TEXT NOT NULL DEFAULT ''`},
		},
	}
}

func TestApplySQLite_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openSQLiteTestDB(t)
	steps := fixtureSteps()

	require.NoError(t, migrate.ApplySQLite(ctx, db, steps))
	n1, err := migrate.AppliedSQLite(ctx, db)
	require.NoError(t, err)
	require.Equal(t, 2, n1)

	require.NoError(t, migrate.ApplySQLite(ctx, db, steps))
	n2, err := migrate.AppliedSQLite(ctx, db)
	require.NoError(t, err)
	require.Equal(t, 2, n2)

	var colorExists int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('widget') WHERE name = 'color'`).Scan(&colorExists)
	require.NoError(t, err)
	require.Equal(t, 1, colorExists)
}

func TestApplySQLite_RejectsOutOfOrderIDs(t *testing.T) {
	ctx := context.Background()
	db := openSQLiteTestDB(t)
	steps := []migrate.Step{
		{ID: 2, Name: "second", Statements: []string{`SELECT 1`}},
		{ID: 1, Name: "first", Statements: []string{`SELECT 1`}},
	}
	err := migrate.ApplySQLite(ctx, db, steps)
	require.Error(t, err)
	require.True(t, errors.Is(err, migrate.ErrOutOfOrder))
}

func TestApplySQLite_StepWithoutIFNotExists(t *testing.T) {
	ctx := context.Background()
	db := openSQLiteTestDB(t)
	steps := []migrate.Step{
		{
			ID:         1,
			Name:       "create_strict",
			Statements: []string{`CREATE TABLE strict_table (id INTEGER PRIMARY KEY)`},
		},
	}
	require.NoError(t, migrate.ApplySQLite(ctx, db, steps))
	// Force a retry of the same non-idempotent step (e.g. crash before commit recorded).
	_, err := db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE id = 1`)
	require.NoError(t, err)
	err = migrate.ApplySQLite(ctx, db, steps)
	require.Error(t, err)
}
