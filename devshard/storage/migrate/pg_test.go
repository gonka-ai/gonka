package migrate_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"devshard/storage/migrate"
)

func testPGPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping postgres migration tests in -short mode (requires Docker)")
	}
	if os.Getenv("TEST_PG_DSN") == "" && os.Getenv("PGHOST") == "" {
		// Spin up testcontainers when no external Postgres is configured.
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
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "")
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	t.Cleanup(pool.Close)
	return pool
}

func TestApplyPG_Idempotent(t *testing.T) {
	ctx := context.Background()
	pool := testPGPool(t)
	steps := fixtureSteps()

	require.NoError(t, migrate.ApplyPG(ctx, pool, steps))
	n1, err := migrate.AppliedPG(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, 2, n1)

	require.NoError(t, migrate.ApplyPG(ctx, pool, steps))
	n2, err := migrate.AppliedPG(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, 2, n2)

	exists, err := migrate.TableExistsPG(ctx, pool, "widget")
	require.NoError(t, err)
	require.True(t, exists)
}

func TestApplyPG_RejectsOutOfOrderIDs(t *testing.T) {
	ctx := context.Background()
	pool := testPGPool(t)
	steps := []migrate.Step{
		{ID: 2, Name: "second", SQL: `SELECT 1`},
		{ID: 1, Name: "first", SQL: `SELECT 1`},
	}
	err := migrate.ApplyPG(ctx, pool, steps)
	require.Error(t, err)
	require.True(t, errors.Is(err, migrate.ErrOutOfOrder))
}

func TestApplyPG_StepWithoutIFNotExists(t *testing.T) {
	ctx := context.Background()
	pool := testPGPool(t)
	steps := []migrate.Step{
		{
			ID:   1,
			Name: "create_strict",
			SQL:  `CREATE TABLE strict_table (id INT PRIMARY KEY)`,
		},
	}
	require.NoError(t, migrate.ApplyPG(ctx, pool, steps))
	_, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE id = 1`)
	require.NoError(t, err)
	err = migrate.ApplyPG(ctx, pool, steps)
	require.Error(t, err)
}

func TestApplyPG_TransactionRollback(t *testing.T) {
	ctx := context.Background()
	pool := testPGPool(t)

	steps := []migrate.Step{
		{
			ID:   1,
			Name: "fail_mid_tx",
			SQL: `
CREATE TABLE migrate_rollback_probe (id INT PRIMARY KEY);
INSERT INTO migrate_rollback_probe (id) VALUES (1);
INSERT INTO migrate_rollback_probe (id) VALUES (1);
`,
		},
	}
	err := migrate.ApplyPG(ctx, pool, steps)
	require.Error(t, err)

	n, err := migrate.AppliedPG(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	exists, err := migrate.TableExistsPG(ctx, pool, "migrate_rollback_probe")
	require.NoError(t, err)
	require.False(t, exists)
}
