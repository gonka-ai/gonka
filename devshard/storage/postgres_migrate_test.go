package storage

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"devshard/storage/migrate"
)

type postgresPartitionDDLTracer struct {
	mu    sync.Mutex
	count int
}

func (t *postgresPartitionDDLTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.record(data.SQL)
	return ctx
}

func (t *postgresPartitionDDLTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *postgresPartitionDDLTracer) record(sql string) {
	upper := strings.ToUpper(sql)
	if strings.Contains(upper, "CREATE TABLE") && strings.Contains(upper, "PARTITION OF") {
		t.mu.Lock()
		t.count++
		t.mu.Unlock()
	}
}

func (t *postgresPartitionDDLTracer) partitionDDLCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

func setupDevshardPostgresPool(t *testing.T, tracer pgx.QueryTracer) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping postgres devshard migration tests in -short mode (requires Docker)")
	}

	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:18.1-bookworm",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(postgresContainerWaitStrategy()),
	)
	require.NoError(t, err)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)

	t.Setenv("PGHOST", host)
	t.Setenv("PGPORT", port.Port())
	t.Setenv("PGDATABASE", "testdb")
	t.Setenv("PGUSER", "testuser")
	t.Setenv("PGPASSWORD", "testpass")

	cfg, err := pgxpool.ParseConfig("")
	require.NoError(t, err)
	if tracer != nil {
		cfg.ConnConfig.Tracer = tracer
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))

	cleanup := func() {
		pool.Close()
		_ = container.Terminate(ctx)
	}
	return pool, cleanup
}

func TestMigratePostgres_Idempotent(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := setupDevshardPostgresPool(t, nil)
	defer cleanup()

	require.NoError(t, MigratePostgres(ctx, pool))
	n1, err := migrate.AppliedPG(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, len(PostgresMigrationSteps()), n1)

	parents := append([]string{pgSessionIndex}, postgresPartitionedParents...)
	for _, table := range parents {
		exists, err := migrate.TableExistsPG(ctx, pool, table)
		require.NoError(t, err)
		require.True(t, exists, "missing table %s", table)
	}

	var indexCount int
	err = pool.QueryRow(ctx, `
SELECT COUNT(*) FROM pg_indexes
WHERE schemaname = 'public' AND indexname = 'devshard_session_index_by_epoch'`).Scan(&indexCount)
	require.NoError(t, err)
	require.Equal(t, 1, indexCount)

	require.NoError(t, MigratePostgres(ctx, pool))
	n2, err := migrate.AppliedPG(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, n1, n2)
}

func TestSaveSnapshot_SameEpoch_PartitionCreateOnce(t *testing.T) {
	ctx := context.Background()
	tracer := &postgresPartitionDDLTracer{}
	pool, cleanup := setupDevshardPostgresPool(t, tracer)
	defer cleanup()

	pg := &Postgres{
		pool:        pool,
		knownEpochs: make(map[uint64]struct{}),
		escrowIdx:   make(map[string]uint64),
	}
	require.NoError(t, MigratePostgres(ctx, pool))

	const epochID = uint64(42)
	require.NoError(t, pg.CreateSession(paramsForEpoch("escrow-snap", epochID)))
	// CreateSession already ran ensurePartition; reset the counter so we only
	// observe DDL issued by SaveSnapshot (the old bug re-created the snapshot
	// partition on every save).
	tracer.mu.Lock()
	tracer.count = 0
	tracer.mu.Unlock()

	require.NoError(t, pg.SaveSnapshot("escrow-snap", 100, []byte("snap-a")))
	require.NoError(t, pg.SaveSnapshot("escrow-snap", 200, []byte("snap-b")))
	require.Equal(t, 0, tracer.partitionDDLCount(),
		"SaveSnapshot must not issue PARTITION OF DDL when partitions already exist for the epoch")
}

func TestEnsurePartition_CreatesAllParentPartitionsOnce(t *testing.T) {
	ctx := context.Background()
	tracer := &postgresPartitionDDLTracer{}
	pool, cleanup := setupDevshardPostgresPool(t, tracer)
	defer cleanup()

	pg := &Postgres{
		pool:        pool,
		knownEpochs: make(map[uint64]struct{}),
		escrowIdx:   make(map[string]uint64),
	}
	require.NoError(t, MigratePostgres(ctx, pool))

	want := len(postgresPartitionedParents)
	require.NoError(t, pg.ensurePartition(ctx, 77))
	require.Equal(t, want, tracer.partitionDDLCount(),
		"first ensurePartition should create one child per partitioned parent (%d tables)", want)

	require.NoError(t, pg.ensurePartition(ctx, 77))
	require.Equal(t, want, tracer.partitionDDLCount(),
		"second ensurePartition must not issue partition DDL")
}
