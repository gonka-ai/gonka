package migrate_test

import (
	"context"
	"errors"
	"fmt"
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

	ctx := context.Background()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		// Always use an isolated container. Do not honor shell PGHOST/PGPORT —
		// a developer's local devshard DB leaves schema_migrations rows that
		// break these unit tests.
		container, err := postgres.Run(ctx,
			"postgres:18.1-bookworm",
			postgres.WithDatabase("testdb"),
			postgres.WithUsername("testuser"),
			postgres.WithPassword("testpass"),
			testcontainers.WithWaitStrategy(
				wait.ForAll(
					wait.ForLog("database system is ready to accept connections").
						WithOccurrence(2),
					wait.ForListeningPort("5432/tcp"),
				).WithStartupTimeout(60*time.Second),
			),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = container.Terminate(ctx) })

		host, err := container.Host(ctx)
		require.NoError(t, err)
		port, err := container.MappedPort(ctx, "5432/tcp")
		require.NoError(t, err)

		dsn = fmt.Sprintf("postgres://testuser:testpass@%s:%s/testdb?sslmode=disable", host, port.Port())
	}

	pool, err := pgxpool.New(ctx, dsn)
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
		{ID: 2, Name: "second", Statements: []string{`SELECT 1`}},
		{ID: 1, Name: "first", Statements: []string{`SELECT 1`}},
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
			ID:         1,
			Name:       "create_strict",
			Statements: []string{`CREATE TABLE strict_table (id INT PRIMARY KEY)`},
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
			Statements: []string{
				`CREATE TABLE migrate_rollback_probe (id INT PRIMARY KEY)`,
				`INSERT INTO migrate_rollback_probe (id) VALUES (1)`,
				`INSERT INTO migrate_rollback_probe (id) VALUES (1)`,
			},
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
