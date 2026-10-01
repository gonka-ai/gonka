package accounting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupAccountingPostgres(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping postgres testcontainers tests in -short mode (requires Docker)")
	}

	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:18.1-bookworm",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	t.Setenv("PGHOST", host)
	t.Setenv("PGPORT", port.Port())
	t.Setenv("PGDATABASE", "testdb")
	t.Setenv("PGUSER", "testuser")
	t.Setenv("PGPASSWORD", "testpass")
	t.Setenv("DEVSHARD_STORAGE_MODE", "postgres")
}

func TestPostgresAccountingRoundTrip(t *testing.T) {
	setupAccountingPostgres(t)
	path := filepath.Join(t.TempDir(), "accounting.db")

	tr, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	tr.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, tr, "e-pg-1", 8, "m")
	require.NoError(t, tr.RecordDiff("e-pg-1", 1, true))
	require.NoError(t, tr.RecordRealSend("e-pg-1", 1, accountingTestNow, PhaseNormal, QuarantineNone))
	require.NoError(t, tr.Flush(context.Background()))
	require.NoError(t, tr.Close())

	reopened, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	defer reopened.Close()
	record := onlyRecord(t, reopened.Query(QueryFilter{EpochIndex: 8}), "p1")
	require.Equal(t, uint64(1), record.Unclassified)
}

func TestPostgresAccountingMigratesFromSQLite(t *testing.T) {
	setupAccountingPostgres(t)
	path := filepath.Join(t.TempDir(), "accounting.db")

	// Seed SQLite while PG is "disabled" via mode=sqlite.
	t.Setenv("DEVSHARD_STORAGE_MODE", "sqlite")
	tr, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	tr.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, tr, "e-mig", 9, "m")
	require.NoError(t, tr.RecordDiff("e-mig", 1, true))
	require.NoError(t, tr.Close())

	t.Setenv("DEVSHARD_STORAGE_MODE", "postgres")
	reopened, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	defer reopened.Close()
	record := onlyRecord(t, reopened.Query(QueryFilter{EpochIndex: 9}), "p1")
	require.Equal(t, uint64(1), record.Unclassified)
}

func TestPostgresAccountingDirtyUpsertDoesNotWipePeerEscrow(t *testing.T) {
	setupAccountingPostgres(t)
	path := filepath.Join(t.TempDir(), "accounting.db")

	a, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	a.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, a, "e-a", 10, "m")
	require.NoError(t, a.Flush(context.Background()))

	b, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	b.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, b, "e-b", 10, "m")
	require.NoError(t, b.Flush(context.Background()))
	require.NoError(t, b.Close())

	// Instance A only dirties its own escrow; peer row must remain.
	require.NoError(t, a.RecordDiff("e-a", 1, true))
	require.NoError(t, a.Flush(context.Background()))
	require.NoError(t, a.Close())

	reopened, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	defer reopened.Close()
	require.NotNil(t, reopened.escrows["e-a"])
	require.NotNil(t, reopened.escrows["e-b"])
}
