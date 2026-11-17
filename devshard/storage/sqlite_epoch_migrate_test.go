package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/storage/migrate"

	_ "modernc.org/sqlite"
)

func openEpochTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "epoch_1.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrateEpochPool_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openEpochTestDB(t)

	require.NoError(t, MigrateEpochPool(ctx, db))
	n1, err := migrate.AppliedSQLite(ctx, db)
	require.NoError(t, err)
	require.Equal(t, len(SQLiteEpochMigrationSteps()), n1)

	require.NoError(t, MigrateEpochPool(ctx, db))
	n2, err := migrate.AppliedSQLite(ctx, db)
	require.NoError(t, err)
	require.Equal(t, n1, n2)
}

func TestOpenEpochPool_MigrationsRunOnce(t *testing.T) {
	dir := t.TempDir()
	db, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.CreateSession(paramsForEpoch("e1", 5)))
	require.NoError(t, db.CreateSession(paramsForEpoch("e2", 5)))

	epochPath := filepath.Join(dir, "epoch_5.db")
	epochDB, err := sql.Open("sqlite", epochPath)
	require.NoError(t, err)
	defer epochDB.Close()

	ctx := context.Background()
	n1, err := migrate.AppliedSQLite(ctx, epochDB)
	require.NoError(t, err)
	require.Equal(t, len(SQLiteEpochMigrationSteps()), n1)

	_, err = db.openOrLoadPool(5)
	require.NoError(t, err)

	n2, err := migrate.AppliedSQLite(ctx, epochDB)
	require.NoError(t, err)
	require.Equal(t, n1, n2)
}

func TestMigrateEpochPool_FromHeaderTimeSchema(t *testing.T) {
	ctx := context.Background()
	db := openEpochTestDB(t)
	steps := SQLiteEpochMigrationSteps()
	var base []migrate.Step
	for _, step := range steps {
		if step.ID <= 9 {
			base = append(base, step)
		}
	}
	require.NoError(t, migrate.ApplySQLite(ctx, db, base))
	require.NoError(t, MigrateEpochPool(ctx, db))
	for _, query := range []string{
		"SELECT header_times_json FROM diffs LIMIT 0",
		"SELECT sealed_usage_challenged FROM sealed_inferences LIMIT 0",
		"SELECT data FROM challenge_votes LIMIT 0",
	} {
		rows, err := db.QueryContext(ctx, query)
		require.NoError(t, err)
		require.NoError(t, rows.Close())
	}
	n, err := migrate.AppliedSQLite(ctx, db)
	require.NoError(t, err)
	require.Equal(t, len(steps), n)
}
