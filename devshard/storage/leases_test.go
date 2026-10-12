package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testOwner(addr string) LeaseOwner {
	return LeaseOwner{Address: addr, InstanceID: addr, Hostname: "host-" + addr}
}

func TestMemoryLease_Acquire_FirstWins(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)

	won, err = store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-2"))
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
			won, err := store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance"))
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

func TestMemoryLease_Acquire_AllowsSameInferenceDifferentEpoch(t *testing.T) {
	runLeaseEpochIdentityTests(t, NewMemory())
}

func TestPostgresLease_Acquire_AllowsSameInferenceDifferentEpoch(t *testing.T) {
	runLeaseEpochIdentityTests(t, newTestPostgres(t))
}

func runLeaseEpochIdentityTests(t *testing.T, store LeaseStore) {
	t.Helper()
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-identity", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)

	won, err = store.Acquire(ctx, "escrow-identity", 1, 11, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won, "same escrow/inference in a different epoch must be a distinct lease")

	won, err = store.Acquire(ctx, "escrow-identity", 1, 10, testOwner("instance-2"))
	require.NoError(t, err)
	require.False(t, won, "same epoch/escrow/inference must still deduplicate")

	owned, err := store.OwnsPendingLease(ctx, "escrow-identity", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, owned)

	require.NoError(t, store.Release(ctx, "escrow-identity", 1, 10, testOwner("instance-1")))
	owned, err = store.OwnsPendingLease(ctx, "escrow-identity", 1, 11, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, owned, "releasing one epoch must not release another")
}

func TestMemoryLease_AcquireOneStale_PicksStale(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	ageMemoryLease(t, store, "escrow-1", 1, 10, time.Hour)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-1", testOwner("instance-2"), 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceID)
	require.Equal(t, uint64(10), epochID)
}

func TestMemoryLease_AcquireOneStale_PicksStaleSubmitted(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-submitted", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, store.SetResult(ctx, "escrow-submitted", 1, 10, LeaseStatusSubmitted, testOwner("instance-1")))
	ageMemoryLease(t, store, "escrow-submitted", 1, 10, time.Hour)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-submitted", testOwner("instance-2"), 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceID)
	require.Equal(t, uint64(10), epochID)

	owned, err := store.OwnsPendingLease(ctx, "escrow-submitted", 1, 10, testOwner("instance-2"))
	require.NoError(t, err)
	require.True(t, owned)
}

func TestMemoryLease_SetResult_RequiresOwner(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, testOwner("instance-2"))
	require.ErrorIs(t, err, ErrLeaseNotOwned)

	owned, err := store.OwnsPendingLease(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, owned)

	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, testOwner("instance-1")))
	owned, err = store.OwnsPendingLease(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.False(t, owned)
}

func TestMemoryLease_SetResult_RejectsAfterStaleSteal(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	_, err := store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	ageMemoryLease(t, store, "escrow-1", 1, 10, time.Hour)

	_, _, err = store.AcquireOneStale(ctx, "escrow-1", testOwner("instance-2"), 30*time.Minute)
	require.NoError(t, err)

	err = store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, testOwner("instance-1"))
	require.ErrorIs(t, err, ErrLeaseNotOwned)
	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, testOwner("instance-2")))
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

	won, err := store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)

	// No dedup: a second acquire on the same pair still grants.
	won, err = store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-2"))
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
	_, err := store.Acquire(ctx, "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-1", testOwner("instance-2"), 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(0), inferenceID)
	require.Equal(t, uint64(0), epochID)
}

func TestSQLiteLease_SetResult_NoOp(t *testing.T) {
	store := newTestSQLite(t)
	ctx := context.Background()

	require.NoError(t, store.SetResult(ctx, "escrow-1", 1, 10, LeaseStatusSubmitted, testOwner("instance-1")))
}

func TestSQLiteLease_Release_NoOp(t *testing.T) {
	store := newTestSQLite(t)
	ctx := context.Background()

	require.NoError(t, store.Release(ctx, "escrow-1", 1, 10, testOwner("instance-1")))
}

func TestMemoryLease_Release(t *testing.T) {
	runLeaseReleaseTests(t, NewMemory())
}

func TestPostgresLease_Release(t *testing.T) {
	runLeaseReleaseTests(t, newTestPostgres(t))
}

func TestMemoryLease_Describe(t *testing.T) {
	runLeaseDescribeTests(t, NewMemory())
}

func TestPostgresLease_Describe(t *testing.T) {
	runLeaseDescribeTests(t, newTestPostgres(t))
}

// runLeaseDescribeTests covers the read that explains a refused Acquire. It is
// the only lease call that reports a row it does not own, so the assertions are
// about reporting the row faithfully rather than about ownership.
func runLeaseDescribeTests(t *testing.T, store LeaseStore) {
	t.Helper()
	ctx := context.Background()

	_, found, err := store.DescribeLease(ctx, "escrow-describe", 1, 10)
	require.NoError(t, err)
	require.False(t, found, "no row yet")

	before := time.Now()
	won, err := store.Acquire(ctx, "escrow-describe", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)

	info, found, err := store.DescribeLease(ctx, "escrow-describe", 1, 10)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "instance-1", info.InstanceAddr)
	require.Equal(t, "instance-1", info.InstanceID)
	require.Equal(t, "host-instance-1", info.Hostname)
	require.Equal(t, LeaseStatusPending, info.Status)
	require.False(t, info.ClaimedAt.Before(before.Truncate(time.Second)), "claimed_at must be populated")

	// A second acquire is refused; describing it must report the holder rather
	// than the caller, which is the whole point of the call.
	won, err = store.Acquire(ctx, "escrow-describe", 1, 10, testOwner("instance-2"))
	require.NoError(t, err)
	require.False(t, won)
	info, found, err = store.DescribeLease(ctx, "escrow-describe", 1, 10)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "instance-1", info.InstanceAddr)
	require.Equal(t, "instance-1", info.InstanceID)
	require.Equal(t, "host-instance-1", info.Hostname)

	require.NoError(t, store.SetResult(ctx, "escrow-describe", 1, 10, LeaseStatusSubmitted, testOwner("instance-1")))
	info, found, err = store.DescribeLease(ctx, "escrow-describe", 1, 10)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, LeaseStatusSubmitted, info.Status)

	// The epoch is part of the key, so a describe for another epoch must not
	// report this row.
	_, found, err = store.DescribeLease(ctx, "escrow-describe", 1, 11)
	require.NoError(t, err)
	require.False(t, found)
}

// TestSQLiteLease_Describe pins the documented no-op: SQLite always grants, so
// nothing ever reaches the diagnosis path and there is no row to report.
func TestSQLiteLease_Describe(t *testing.T) {
	store, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	won, err := store.Acquire(context.Background(), "escrow-1", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)

	_, found, err := store.DescribeLease(context.Background(), "escrow-1", 1, 10)
	require.NoError(t, err)
	require.False(t, found)
}

func TestPostgresLease_AcquireOneStale_PicksStaleSubmitted(t *testing.T) {
	store := newTestPostgres(t)
	ctx := context.Background()

	won, err := store.Acquire(ctx, "escrow-submitted", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, store.SetResult(ctx, "escrow-submitted", 1, 10, LeaseStatusSubmitted, testOwner("instance-1")))
	_, err = store.pool.Exec(ctx,
		`UPDATE devshard_validation_leases
		 SET claimed_at = now() - interval '1 hour'
		 WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`,
		uint64(10), "escrow-submitted", uint64(1),
	)
	require.NoError(t, err)

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-submitted", testOwner("instance-2"), 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, uint64(1), inferenceID)
	require.Equal(t, uint64(10), epochID)

	owned, err := store.OwnsPendingLease(ctx, "escrow-submitted", 1, 10, testOwner("instance-2"))
	require.NoError(t, err)
	require.True(t, owned)
}

func runLeaseReleaseTests(t *testing.T, store LeaseStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("released row is re-acquirable", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-rel", 1, 10, testOwner("instance-1"))
		require.NoError(t, err)
		require.True(t, won)

		require.NoError(t, store.Release(ctx, "escrow-rel", 1, 10, testOwner("instance-1")))

		won, err = store.Acquire(ctx, "escrow-rel", 1, 10, testOwner("instance-2"))
		require.NoError(t, err)
		require.True(t, won)
	})

	t.Run("non-owner is no-op", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-owner", 1, 10, testOwner("instance-1"))
		require.NoError(t, err)
		require.True(t, won)

		require.NoError(t, store.Release(ctx, "escrow-owner", 1, 10, testOwner("instance-2")))

		won, err = store.Acquire(ctx, "escrow-owner", 1, 10, testOwner("instance-2"))
		require.NoError(t, err)
		require.False(t, won)
	})

	t.Run("submitted is no-op", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-sub", 1, 10, testOwner("instance-1"))
		require.NoError(t, err)
		require.True(t, won)
		require.NoError(t, store.SetResult(ctx, "escrow-sub", 1, 10, LeaseStatusSubmitted, testOwner("instance-1")))

		require.NoError(t, store.Release(ctx, "escrow-sub", 1, 10, testOwner("instance-1")))

		won, err = store.Acquire(ctx, "escrow-sub", 1, 10, testOwner("instance-2"))
		require.NoError(t, err)
		require.False(t, won)
	})

	t.Run("wrong epoch is no-op", func(t *testing.T) {
		won, err := store.Acquire(ctx, "escrow-epoch", 1, 10, testOwner("instance-1"))
		require.NoError(t, err)
		require.True(t, won)

		require.NoError(t, store.Release(ctx, "escrow-epoch", 1, 11, testOwner("instance-1")))

		won, err = store.Acquire(ctx, "escrow-epoch", 1, 10, testOwner("instance-2"))
		require.NoError(t, err)
		require.False(t, won)
	})

	t.Run("missing is no-op", func(t *testing.T) {
		require.NoError(t, store.Release(ctx, "escrow-missing", 99, 10, testOwner("instance-1")))
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

	won, err := store.Acquire(ctx, "escrow-ep", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, won)

	owned, err := store.OwnsPendingLease(ctx, "escrow-ep", 1, 11, testOwner("instance-1"))
	require.NoError(t, err)
	require.False(t, owned, "wrong epoch must not match")

	owned, err = store.OwnsPendingLease(ctx, "escrow-ep", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.True(t, owned)

	err = store.SetResult(ctx, "escrow-ep", 1, 11, LeaseStatusSubmitted, testOwner("instance-1"))
	require.ErrorIs(t, err, ErrLeaseNotOwned)

	require.NoError(t, store.SetResult(ctx, "escrow-ep", 1, 10, LeaseStatusSubmitted, testOwner("instance-1")))
	owned, err = store.OwnsPendingLease(ctx, "escrow-ep", 1, 10, testOwner("instance-1"))
	require.NoError(t, err)
	require.False(t, owned)
}

func TestMemoryLease_Identity(t *testing.T) {
	runLeaseIdentityTests(t, NewMemory())
}

func TestPostgresLease_Identity(t *testing.T) {
	runLeaseIdentityTests(t, newTestPostgres(t))
}

// runLeaseIdentityTests covers process identity. Same signer address with a
// different instance id is a different holder. Hostname never decides
// ownership. A blank instance id, which is what a pre-identity row carries,
// is owned by nobody and is still reclaimable once it is stale.
func runLeaseIdentityTests(t *testing.T, store LeaseStore) {
	t.Helper()
	ctx := context.Background()
	shared := LeaseOwner{Address: "gonka1", InstanceID: "proc-a", Hostname: "versiond"}
	sibling := LeaseOwner{Address: "gonka1", InstanceID: "proc-b", Hostname: "versiond"}
	renamed := LeaseOwner{Address: "gonka1", InstanceID: "proc-a", Hostname: "versiond2"}

	won, err := store.Acquire(ctx, "escrow-id", 1, 10, shared)
	require.NoError(t, err)
	require.True(t, won)

	require.NoError(t, store.Release(ctx, "escrow-id", 1, 10, sibling))
	owned, err := store.OwnsPendingLease(ctx, "escrow-id", 1, 10, shared)
	require.NoError(t, err)
	require.True(t, owned, "a sibling with the same address must not release this row")

	err = store.SetResult(ctx, "escrow-id", 1, 10, LeaseStatusSubmitted, sibling)
	require.ErrorIs(t, err, ErrLeaseNotOwned)
	owned, err = store.OwnsPendingLease(ctx, "escrow-id", 1, 10, sibling)
	require.NoError(t, err)
	require.False(t, owned)

	owned, err = store.OwnsPendingLease(ctx, "escrow-id", 1, 10, renamed)
	require.NoError(t, err)
	require.True(t, owned, "hostname is not part of ownership")
	require.NoError(t, store.SetResult(ctx, "escrow-id", 1, 10, LeaseStatusSubmitted, renamed))

	legacy := LeaseOwner{Address: "gonka1"}
	won, err = store.Acquire(ctx, "escrow-legacy", 2, 10, legacy)
	require.NoError(t, err)
	require.True(t, won)
	current := LeaseOwner{Address: "gonka1", InstanceID: "proc-now", Hostname: "versiond"}
	owned, err = store.OwnsPendingLease(ctx, "escrow-legacy", 2, 10, current)
	require.NoError(t, err)
	require.False(t, owned, "a blank instance id is not owned")
	owned, err = store.OwnsPendingLease(ctx, "escrow-legacy", 2, 10, legacy)
	require.NoError(t, err)
	require.False(t, owned, "the writer of a blank instance id does not own it either")
	err = store.SetResult(ctx, "escrow-legacy", 2, 10, LeaseStatusSkipped, current)
	require.ErrorIs(t, err, ErrLeaseNotOwned)
	require.NoError(t, store.Release(ctx, "escrow-legacy", 2, 10, current))

	inferenceID, epochID, err := store.AcquireOneStale(ctx, "escrow-legacy", current, -time.Second)
	require.NoError(t, err)
	require.Equal(t, uint64(2), inferenceID)
	require.Equal(t, uint64(10), epochID)
	owned, err = store.OwnsPendingLease(ctx, "escrow-legacy", 2, 10, current)
	require.NoError(t, err)
	require.True(t, owned, "reclaim writes the new process id")
	owned, err = store.OwnsPendingLease(ctx, "escrow-legacy", 2, 10, legacy)
	require.NoError(t, err)
	require.False(t, owned)

	info, found, err := store.DescribeLease(ctx, "escrow-legacy", 2, 10)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, current.InstanceID, info.InstanceID)
	require.Equal(t, current.Hostname, info.Hostname)
}
