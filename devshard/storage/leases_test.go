package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryLease_Acquire_FirstWins(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	won, err = store.Acquire(ctx, "escrow-1", 1, 10, "instance-2")
	require.NoError(t, err)
	require.False(t, won)
}

func TestMemoryLease_Acquire_ConcurrentSingleWinner(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	const workers = 8
	var wg sync.WaitGroup
	wins := make(chan bool, workers)
	for range workers {
		wg.Go(func() {
			won, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance")
			require.NoError(t, err)
			wins <- won
		})
	}
	wg.Wait()
	close(wins)

	winCount := 0
	for won := range wins {
		if won {
			winCount++
		}
	}
	require.Equal(t, 1, winCount)
}

func TestMemoryLease_Acquire_AllowsSameInferenceDifferentEpoch(t *testing.T) {
	runLeaseEpochIdentityTests(t, NewMemory())
}

func TestPostgresLease_Acquire_AllowsSameInferenceDifferentEpoch(t *testing.T) {
	runLeaseEpochIdentityTests(t, newTestPostgres(t))
}

func runLeaseEpochIdentityTests(t *testing.T, store LeaseStore) {
	t.Helper()
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-identity", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	won, err = store.Acquire(ctx, "escrow-identity", 1, 11, "instance-1")
	require.NoError(t, err)
	require.True(t, won, "same escrow/inference in a different epoch must be a distinct lease")

	won, err = store.Acquire(ctx, "escrow-identity", 1, 10, "instance-2")
	require.NoError(t, err)
	require.False(t, won, "same epoch/escrow/inference must still deduplicate")

	owned, err := store.OwnsPendingLease(ctx, "escrow-identity", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, owned)

	require.NoError(t, store.Release(ctx, "escrow-identity", 1, 10, "instance-1"))
	owned, err = store.OwnsPendingLease(ctx, "escrow-identity", 1, 11, "instance-1")
	require.NoError(t, err)
	require.True(t, owned, "releasing one epoch must not release another")
}

func TestMemoryLease_AcquireOneStale_PicksStale(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	ageMemoryLease(t, store, "escrow-1", 1, 10, time.Hour)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceID)
	require.Equal(t, uint64(10), epochID)
}

func TestMemoryLease_AcquireOneStale_PicksStaleSubmitted(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-submitted", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, store.SetResult(ctx, "escrow-submitted", 1, 10, LeaseStatusSubmitted, "instance-1"))
	ageMemoryLease(t, store, "escrow-submitted", 1, 10, time.Hour)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-submitted", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceID)
	require.Equal(t, uint64(10), epochID)

	owned, err := store.OwnsPendingLease(ctx, "escrow-submitted", 1, 10, "instance-2")
	require.NoError(t, err)
	require.True(t, owned)
}

func TestMemoryLease_SetResult_RequiresOwner(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, "instance-2")
	require.ErrorIs(t, err, ErrLeaseNotOwned)

	owned, err := store.OwnsPendingLease(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, owned)

	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, "instance-1"))
	owned, err = store.OwnsPendingLease(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	require.False(t, owned)
}

func TestMemoryLease_SetResult_RejectsAfterStaleSteal(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	ageMemoryLease(t, store, "escrow-1", 1, 10, time.Hour)

	_, _, err = store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, "instance-1")
	require.ErrorIs(t, err, ErrLeaseNotOwned)
	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, "instance-2"))
}

func ageMemoryLease(t *testing.T, store *Memory, escrowID string, inferenceID, epochID uint64, age time.Duration) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	key := memoryLeaseKey{epochID: epochID, inferenceID: inferenceID}
	lease, ok := store.validationLeases[escrowID][key]
	require.True(t, ok)
	lease.claimedAt = time.Now().Add(-age)
	store.validationLeases[escrowID][key] = lease
}

// SQLite is single-instance, so its lease store is a deliberate no-op: Acquire
// always grants (validation runs inline) and AcquireOneStale/SetResult do
// nothing. Cross-instance dedup is only meaningful on Postgres. See
// storage/leases.go and docs/rolling-update.md.

func TestSQLiteLease_Acquire_AlwaysGrants(t *testing.T) {
	store := newTestSQLite(t)
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	// No dedup: a second acquire on the same pair still grants.
	won, err = store.Acquire(ctx, "escrow-1", 1, 10, "instance-2")
	require.NoError(t, err)
	require.True(t, won)
}

func TestSQLiteLease_AcquireOneStale_NoOp(t *testing.T) {
	store := newTestSQLite(t)
	ctx := context.Background()

	require.NoError(t, store.CreateSession(CreateSessionParams{
		EscrowID: "escrow-1",
		EpochID:  10,
		Version:  "v1",
	}))
	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(0), inferenceID)
	require.Equal(t, uint64(0), epochID)
}

func TestSQLiteLease_SetResult_NoOp(t *testing.T) {
	store := newTestSQLite(t)
	ctx := context.Background()

	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, "instance-1"))
}

func TestSQLiteLease_Release_NoOp(t *testing.T) {
	store := newTestSQLite(t)
	ctx := context.Background()

	require.NoError(t, store.Release(ctx, "escrow-1", 1, 10, "instance-1"))
}

func TestMemoryLease_Release(t *testing.T) {
	runLeaseReleaseTests(t, NewMemory())
}

func TestPostgresLease_Release(t *testing.T) {
	runLeaseReleaseTests(t, newTestPostgres(t))
}

func TestPostgresLease_AcquireOneStale_PicksStaleSubmitted(t *testing.T) {
	store := newTestPostgres(t)
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-submitted", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, store.SetResult(ctx, "escrow-submitted", 1, 10, LeaseStatusSubmitted, "instance-1"))
	_, err = store.pool.Exec(ctx,
		`UPDATE devshard_validation_leases
		 SET claimed_at = now() - interval '1 hour'
		 WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`,
		uint64(10), "escrow-submitted", uint64(1),
	)
	require.NoError(t, err)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-submitted", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceID)
	require.Equal(t, uint64(10), epochID)

	owned, err := store.OwnsPendingLease(ctx, "escrow-submitted", 1, 10, "instance-2")
	require.NoError(t, err)
	require.True(t, owned)
}

func runLeaseReleaseTests(t *testing.T, store LeaseStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("released row is re-acquirable", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-rel", 1, 10, "instance-1")
		require.NoError(t, err)
		require.True(t, won)

		require.NoError(t, store.Release(ctx, "escrow-rel", 1, 10, "instance-1"))

		won, err = store.Acquire(ctx, "escrow-rel", 1, 10, "instance-2")
		require.NoError(t, err)
		require.True(t, won)
	})

	t.Run("non-owner is no-op", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-owner", 1, 10, "instance-1")
		require.NoError(t, err)
		require.True(t, won)

		require.NoError(t, store.Release(ctx, "escrow-owner", 1, 10, "instance-2"))

		won, err = store.Acquire(ctx, "escrow-owner", 1, 10, "instance-2")
		require.NoError(t, err)
		require.False(t, won)
	})

	t.Run("submitted is no-op", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-sub", 1, 10, "instance-1")
		require.NoError(t, err)
		require.True(t, won)
		require.NoError(t, store.SetResult(ctx, "escrow-sub", 1, 10, LeaseStatusSubmitted, "instance-1"))

		require.NoError(t, store.Release(ctx, "escrow-sub", 1, 10, "instance-1"))

		won, err = store.Acquire(ctx, "escrow-sub", 1, 10, "instance-2")
		require.NoError(t, err)
		require.False(t, won)
	})

	t.Run("wrong epoch is no-op", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-epoch", 1, 10, "instance-1")
		require.NoError(t, err)
		require.True(t, won)

		require.NoError(t, store.Release(ctx, "escrow-epoch", 1, 11, "instance-1"))

		won, err = store.Acquire(ctx, "escrow-epoch", 1, 10, "instance-2")
		require.NoError(t, err)
		require.False(t, won)
	})

	t.Run("missing is no-op", func(t *testing.T) {
		require.NoError(t, store.Release(ctx, "escrow-missing", 99, 10, "instance-1"))
	})
}

func TestMemoryLease_SetResultAndOwns_RequireEpoch(t *testing.T) {
	runLeaseEpochScopeTests(t, NewMemory())
}

func TestPostgresLease_SetResultAndOwns_RequireEpoch(t *testing.T) {
	runLeaseEpochScopeTests(t, newTestPostgres(t))
}

func runLeaseEpochScopeTests(t *testing.T, store LeaseStore) {
	t.Helper()
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-ep", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, won)

	owned, err := store.OwnsPendingLease(ctx, "escrow-ep", 1, 11, "instance-1")
	require.NoError(t, err)
	require.False(t, owned, "wrong epoch must not match")

	owned, err = store.OwnsPendingLease(ctx, "escrow-ep", 1, 10, "instance-1")
	require.NoError(t, err)
	require.True(t, owned)

	err = store.SetResult(ctx, "escrow-ep", 1, 11, LeaseStatusSubmitted, "instance-1")
	require.ErrorIs(t, err, ErrLeaseNotOwned)

	require.NoError(t, store.SetResult(ctx, "escrow-ep", 1, 10, LeaseStatusSubmitted, "instance-1"))
	owned, err = store.OwnsPendingLease(ctx, "escrow-ep", 1, 10, "instance-1")
	require.NoError(t, err)
	require.False(t, owned)
}
