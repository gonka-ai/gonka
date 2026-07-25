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
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance")
			require.NoError(t, err)
			wins <- won
		}()
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

func TestMemoryLease_AcquireOneStale_PicksStale(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	store.mu.Lock()
	lease := store.validationLeases["escrow-1"][1]
	lease.claimedAt = time.Now().Add(-time.Hour)
	store.validationLeases["escrow-1"][1] = lease
	store.mu.Unlock()

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceID)
	require.Equal(t, uint64(10), epochID)
}

func TestMemoryLease_SetResult_RequiresOwner(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, LeaseStatusSubmitted, "instance-2")
	require.ErrorIs(t, err, ErrLeaseNotOwned)

	owned, err := store.OwnsPendingLease(ctx, "escrow-1", 1, "instance-1")
	require.NoError(t, err)
	require.True(t, owned)

	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, LeaseStatusSubmitted, "instance-1"))
	owned, err = store.OwnsPendingLease(ctx, "escrow-1", 1, "instance-1")
	require.NoError(t, err)
	require.False(t, owned)
}

func TestMemoryLease_SetResult_RejectsAfterStaleSteal(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, "instance-1")
	require.NoError(t, err)

	store.mu.Lock()
	lease := store.validationLeases["escrow-1"][1]
	lease.claimedAt = time.Now().Add(-time.Hour)
	store.validationLeases["escrow-1"][1] = lease
	store.mu.Unlock()

	_, _, err = store.AcquireOneStale(ctx, "escrow-1", "instance-2", 30*time.Minute)
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, LeaseStatusSubmitted, "instance-1")
	require.ErrorIs(t, err, ErrLeaseNotOwned)
	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, LeaseStatusSubmitted, "instance-2"))
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

	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, LeaseStatusSubmitted, "instance-1"))
}
