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

func TestMigrateEpochPoolDoesNotInventHostAdmission(t *testing.T) {
	ctx := context.Background()
	db := openEpochTestDB(t)
	require.NoError(t, migrate.ApplySQLite(ctx, db, sqliteEpochMigrationSteps[:8]))
	_, err := db.Exec(`INSERT INTO sessions (escrow_id,version,creator_addr,config_json,group_json,initial_balance,latest_nonce) VALUES ('old-v7','v7','creator','{}','[]',1000,10)`)
	require.NoError(t, err)
	require.NoError(t, MigrateEpochPool(ctx, db))
	var nonce uint64
	require.NoError(t, db.QueryRow(`SELECT host_admission_nonce FROM sessions WHERE escrow_id='old-v7'`).Scan(&nonce))
	require.Zero(t, nonce, "old journal length cannot establish host admission")
}
