package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"devshard/storage/migrate"
	"devshard/storage/pgtest"
)

func testPGPool(t *testing.T) *pgxpool.Pool {
	return testPGPoolWithRuntimeParams(t, nil)
}

func testPGPoolWithRuntimeParams(t *testing.T, runtimeParams map[string]string) *pgxpool.Pool {
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
		container := pgtest.MustStart(t, ctx)
		t.Cleanup(func() { _ = container.Terminate(ctx) })

		host, err := container.Host(ctx)
		require.NoError(t, err)
		port, err := container.MappedPort(ctx, "5432/tcp")
		require.NoError(t, err)

		dsn = fmt.Sprintf("postgres://testuser:testpass@%s:%s/testdb?sslmode=disable", host, port.Port())
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	for key, value := range runtimeParams {
		cfg.ConnConfig.RuntimeParams[key] = value
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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

func TestApplyPG_ConcurrentCallersSerialize(t *testing.T) {
	ctx := context.Background()
	pool := testPGPoolWithRuntimeParams(t, map[string]string{
		"lock_timeout":      "50",
		"statement_timeout": "50",
	})

	steps := []migrate.Step{
		{
			ID:   1,
			Name: "concurrent_create",
			Statements: []string{
				`SELECT pg_sleep(0.2)`,
				`CREATE TABLE concurrent_migration_probe (id INT PRIMARY KEY)`,
			},
		},
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- migrate.ApplyPG(ctx, pool, steps)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	n, err := migrate.AppliedPG(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	exists, err := migrate.TableExistsPG(ctx, pool, "concurrent_migration_probe")
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
