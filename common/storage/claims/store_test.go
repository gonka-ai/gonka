package claims_test

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

	"common/storage/claims"
)

func setupStore(t *testing.T) (*claims.Store, *pgxpool.Pool) {
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

	store, err := claims.New(ctx, pool)
	require.NoError(t, err)
	return store, pool
}

func TestStore_Claim_FirstWins(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	// First instance claims
	won, err := store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	// Second instance tries to claim the same escrow/inference pair
	won, err = store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-2")
	require.NoError(t, err)
	require.False(t, won)
}

func TestStore_Claim_SameInferenceDifferentEscrows(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	// Claim with escrow-1
	won, err := store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	// Same inference but different escrow should also win
	won, err = store.Claim(ctx, "escrow-2", "inf-001", 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)
}

func TestStore_Claim_DifferentInferences(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	// Claim first inference
	won, err := store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	// Claim different inference in same escrow should also win
	won, err = store.Claim(ctx, "escrow-1", "inf-002", 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)
}

func TestStore_ReclaimOneStale_NoStale(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()

	// Claim (fresh claim, not stale)
	_, err := store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-1")
	require.NoError(t, err)

	// Try to reclaim with 30min TTL - should find nothing since claim is fresh
	inferenceId, err := store.ReclaimOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "", inferenceId)
}

func TestStore_ReclaimOneStale_PicksStale(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	// Claim
	_, err := store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-1")
	require.NoError(t, err)

	// Backdate claimed_at to 1 hour ago
	_, err = pool.Exec(ctx,
		`UPDATE validation_claims SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", "inf-001",
	)
	require.NoError(t, err)

	// ReclaimOneStale with 30min TTL should pick this stale claim and reassign it.
	inferenceId, err := store.ReclaimOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "inf-001", inferenceId)

	// Verify the row was actually reassigned to instance-2.
	var owner string
	err = pool.QueryRow(ctx,
		`SELECT instance_address FROM validation_claims WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", "inf-001",
	).Scan(&owner)
	require.NoError(t, err)
	require.Equal(t, "instance-2", owner)
}

func TestStore_ReclaimOneStale_ScopedToEscrow(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	// Claim in escrow-2
	_, err := store.Claim(ctx, "escrow-2", "inf-001", 10, "instance-1")
	require.NoError(t, err)

	// Backdate it
	_, err = pool.Exec(ctx,
		`UPDATE validation_claims SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-2", "inf-001",
	)
	require.NoError(t, err)

	// Try to reclaim from escrow-1 (different escrow) - should find nothing
	inferenceId, err := store.ReclaimOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "", inferenceId)
}

func TestStore_ReclaimOneStale_SkipsCompleted(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	// Claim
	_, err := store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-1")
	require.NoError(t, err)

	// Backdate it
	_, err = pool.Exec(ctx,
		`UPDATE validation_claims SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", "inf-001",
	)
	require.NoError(t, err)

	// Set tx_hash (mark as completed)
	err = store.SetTxHash(ctx, "escrow-1", "inf-001", "0xdeadbeef")
	require.NoError(t, err)

	// ReclaimOneStale should skip it since tx_hash is set
	inferenceId, err := store.ReclaimOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "", inferenceId)
}

func TestStore_SetTxHash(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	// Claim
	_, err := store.Claim(ctx, "escrow-1", "inf-001", 10, "instance-1")
	require.NoError(t, err)

	// Set tx_hash
	err = store.SetTxHash(ctx, "escrow-1", "inf-001", "0xdeadbeef")
	require.NoError(t, err)

	// Verify tx_hash was written to the DB.
	var txHash string
	err = pool.QueryRow(ctx,
		`SELECT coalesce(tx_hash, '') FROM validation_claims WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", "inf-001",
	).Scan(&txHash)
	require.NoError(t, err)
	require.Equal(t, "0xdeadbeef", txHash)

	// Backdate claimed_at
	_, err = pool.Exec(ctx,
		`UPDATE validation_claims SET claimed_at = now() - interval '1 hour'
		 WHERE escrow_id = $1 AND inference_id = $2`,
		"escrow-1", "inf-001",
	)
	require.NoError(t, err)

	// ReclaimOneStale should skip it since tx_hash is set.
	inferenceId, err := store.ReclaimOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "", inferenceId)
}

func TestStore_DeleteByEpoch(t *testing.T) {
	store, pool := setupStore(t)
	ctx := context.Background()

	// Claim with epoch 5
	_, err := store.Claim(ctx, "escrow-1", "inf-001", 5, "instance-1")
	require.NoError(t, err)

	// Claim with epoch 10
	_, err = store.Claim(ctx, "escrow-1", "inf-002", 10, "instance-1")
	require.NoError(t, err)

	// Delete all claims where epoch_id < 10 (should delete epoch 5, keep epoch 10)
	err = store.DeleteByEpoch(ctx, 10)
	require.NoError(t, err)

	// Verify epoch 5 was deleted and epoch 10 was retained.
	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM validation_claims`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	var epochId int
	err = pool.QueryRow(ctx, `SELECT epoch_id FROM validation_claims`).Scan(&epochId)
	require.NoError(t, err)
	require.Equal(t, 10, epochId)
}
