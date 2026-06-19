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

func openMetaTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), metaDBFile)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrateMeta_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openMetaTestDB(t)

	require.NoError(t, MigrateMeta(ctx, db))
	n1, err := migrate.AppliedSQLite(ctx, db)
	require.NoError(t, err)
	require.Equal(t, len(SQLiteMetaMigrationSteps()), n1)

	var tableCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'escrow_epoch'`,
	).Scan(&tableCount))
	require.Equal(t, 1, tableCount)

	var indexCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'escrow_epoch_by_epoch'`,
	).Scan(&indexCount))
	require.Equal(t, 1, indexCount)

	require.NoError(t, MigrateMeta(ctx, db))
	n2, err := migrate.AppliedSQLite(ctx, db)
	require.NoError(t, err)
	require.Equal(t, n1, n2)
}
