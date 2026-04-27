package validationlease_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"common/storage/validationlease"
)

func setupStore(t *testing.T) (*validationlease.Store, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping storage tests in -short mode (requires Docker)")
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
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://testuser:testpass@%s:%s/testdb", host, port.Port())
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	store, err := validationlease.New(ctx, pool)
	require.NoError(t, err)
	return store, pool
}

func TestStore_Acquire_FirstWins(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	won, err = store.Acquire(ctx, "escrow-1", 1, 10, "instance-2")
	require.NoError(t, err)
	require.False(t, won)
}

func TestStore_Acquire_SameInferenceDifferentEscrows(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	won, err = store.Acquire(ctx, "escrow-2", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)
}

func TestStore_Acquire_DifferentInferences(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	won, err = store.Acquire(ctx, "escrow-1", 2, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)
}

func TestStore_AcquireOneStale_NoStale(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	inferenceId, _, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(0), inferenceId)
}

func TestStore_AcquireOneStale_PicksStale(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`UPDATE validation_leases SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", 1,
	)
	require.NoError(t, err)

	inferenceId, _, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceId)

	var owner string
	err = pool.QueryRow(ctx,
		`SELECT instance_address FROM validation_leases WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", 1,
	).Scan(&owner)
	require.NoError(t, err)
	require.Equal(t, "instance-2", owner)
}

func TestStore_AcquireOneStale_ScopedToEscrow(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-2", 1, 10, "instance-1")
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`UPDATE validation_leases SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-2", 1,
	)
	require.NoError(t, err)

	inferenceId, _, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(0), inferenceId)
}

func TestStore_AcquireOneStale_SkipsCompleted(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	_, err = pool.Exec(ctx,
		`UPDATE validation_leases SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", 1,
	)
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, validationlease.StatusSubmitted)
	require.NoError(t, err)

	inferenceId, _, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(0), inferenceId)
}

func TestStore_SetResult(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, validationlease.StatusSubmitted)
	require.NoError(t, err)

	var gotStatus string
	err = pool.QueryRow(ctx,
		`SELECT status FROM validation_leases WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", 1,
	).Scan(&gotStatus)
	require.NoError(t, err)
	require.Equal(t, string(validationlease.StatusSubmitted), gotStatus)

	_, err = pool.Exec(ctx,
		`UPDATE validation_leases SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", 1,
	)
	require.NoError(t, err)

	inferenceId, _, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(0), inferenceId)
}

func TestStore_DeleteBeforeEpoch(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 5, "instance-1")
	require.NoError(t, err)

	_, err = store.Acquire(ctx, "escrow-1", 2, 10, "instance-1")
	require.NoError(t, err)

	err = store.DeleteBeforeEpoch(ctx, 10)
	require.NoError(t, err)

	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM validation_leases`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	var epochId int
	err = pool.QueryRow(ctx, `SELECT epoch_id FROM validation_leases`).Scan(&epochId)
	require.NoError(t, err)
	require.Equal(t, 10, epochId)
}
