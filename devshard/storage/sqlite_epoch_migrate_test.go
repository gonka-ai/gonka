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

// writeLegacyEpochDB creates a per-epoch file with the pre-version schema and seeded rows.
func writeLegacyEpochDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "epoch_legacy.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)

	_, err = db.Exec(`
CREATE TABLE sessions (
    escrow_id       TEXT PRIMARY KEY,
    creator_addr    TEXT NOT NULL,
    config_json     TEXT NOT NULL,
    group_json      TEXT NOT NULL,
    initial_balance INTEGER NOT NULL,
    latest_nonce    INTEGER NOT NULL DEFAULT 0,
    last_finalized  INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'active',
    settled_at      INTEGER
);
INSERT INTO sessions (
    escrow_id, creator_addr, config_json, group_json, initial_balance
) VALUES ('escrow-legacy', 'creator', '{}', '[]', 1000);
`)
	require.NoError(t, err)
	return db
}

func TestMigrateEpochPool_FromLegacySchema(t *testing.T) {
	ctx := context.Background()
	db := writeLegacyEpochDB(t)
	defer db.Close()

	var versionCols int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'version'`,
	).Scan(&versionCols))
	require.Equal(t, 0, versionCols)

	require.NoError(t, MigrateEpochPool(ctx, db))

	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'version'`,
	).Scan(&versionCols))
	require.Equal(t, 1, versionCols)

	var balance int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT initial_balance FROM sessions WHERE escrow_id = 'escrow-legacy'`,
	).Scan(&balance))
	require.Equal(t, int64(1000), balance)
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
