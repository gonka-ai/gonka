package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testExecutionClaimLifecycle(t *testing.T, store ExecutionStore) {
	t.Helper()
	ctx := context.Background()

	first, err := store.ClaimExecution(ctx, 7, "escrow-1", 11, "owner-a")
	require.NoError(t, err)
	require.True(t, first.Acquired)
	require.Equal(t, ExecutionClaimed, first.Status)
	require.NotZero(t, first.Fence)

	second, err := store.ClaimExecution(ctx, 7, "escrow-1", 11, "owner-b")
	require.NoError(t, err)
	require.False(t, second.Acquired)
	require.Equal(t, first.Fence, second.Fence)
	require.Equal(t, ExecutionClaimed, second.Status)
	require.ErrorIs(t,
		store.CompleteExecution(ctx, 7, "escrow-1", 11, "owner-a", first.Fence, []byte("too early")),
		ErrExecutionClaimNotOwned,
	)

	require.ErrorIs(t,
		store.MarkExecutionDispatched(ctx, 7, "escrow-1", 11, "owner-b", first.Fence),
		ErrExecutionClaimNotOwned,
	)
	require.NoError(t,
		store.MarkExecutionDispatched(ctx, 7, "escrow-1", 11, "owner-a", first.Fence),
	)
	dispatched, err := store.GetExecution(ctx, 7, "escrow-1", 11)
	require.NoError(t, err)
	require.Equal(t, ExecutionDispatched, dispatched.Status)
	require.ErrorIs(t,
		store.AbandonExecution(ctx, 7, "escrow-1", 11, "owner-a", first.Fence),
		ErrExecutionClaimNotOwned,
	)

	require.ErrorIs(t,
		store.CompleteExecution(ctx, 7, "escrow-1", 11, "owner-b", first.Fence, []byte("wrong owner")),
		ErrExecutionClaimNotOwned,
	)
	require.ErrorIs(t,
		store.CompleteExecution(ctx, 7, "escrow-1", 11, "owner-a", first.Fence+1, []byte("wrong fence")),
		ErrExecutionClaimNotOwned,
	)

	want := []byte(`{"response":"ok"}`)
	require.NoError(t, store.CompleteExecution(ctx, 7, "escrow-1", 11, "owner-a", first.Fence, want))
	require.ErrorIs(t,
		store.CompleteExecution(ctx, 7, "escrow-1", 11, "owner-a", first.Fence, want),
		ErrExecutionClaimNotOwned,
	)

	completed, err := store.GetExecution(ctx, 7, "escrow-1", 11)
	require.NoError(t, err)
	require.Equal(t, ExecutionCompleted, completed.Status)
	require.Equal(t, want, completed.Result)

	replayed, err := store.ClaimExecution(ctx, 7, "escrow-1", 11, "owner-c")
	require.NoError(t, err)
	require.False(t, replayed.Acquired)
	require.Equal(t, ExecutionCompleted, replayed.Status)
	require.Equal(t, want, replayed.Result)

	abandoned, err := store.ClaimExecution(ctx, 7, "escrow-1", 12, "owner-a")
	require.NoError(t, err)
	require.NoError(t,
		store.AbandonExecution(ctx, 7, "escrow-1", 12, "owner-a", abandoned.Fence),
	)
	reacquired, err := store.ClaimExecution(ctx, 7, "escrow-1", 12, "owner-b")
	require.NoError(t, err)
	require.True(t, reacquired.Acquired)
	require.Equal(t, ExecutionClaimed, reacquired.Status)
	require.NotEqual(t, abandoned.Fence, reacquired.Fence)
}

func TestMemoryExecutionClaimLifecycle(t *testing.T) {
	testExecutionClaimLifecycle(t, NewMemory())
}

func TestMemoryExpiredClaimIsFencedBeforeDispatch(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	first, err := store.ClaimExecution(ctx, 7, "escrow-1", 11, "owner-a")
	require.NoError(t, err)

	key := executionKey{epochID: 7, escrowID: "escrow-1", inferenceID: 11}
	store.mu.Lock()
	claim := store.executionClaims[key]
	claim.claimed = time.Now().Add(-executionClaimLease)
	store.executionClaims[key] = claim
	store.mu.Unlock()

	second, err := store.ClaimExecution(ctx, 7, "escrow-1", 11, "owner-b")
	require.NoError(t, err)
	require.True(t, second.Acquired)
	require.Greater(t, second.Fence, first.Fence)
	require.ErrorIs(t,
		store.MarkExecutionDispatched(ctx, 7, "escrow-1", 11, "owner-a", first.Fence),
		ErrExecutionClaimNotOwned,
	)
	require.NoError(t,
		store.MarkExecutionDispatched(ctx, 7, "escrow-1", 11, "owner-b", second.Fence),
	)
}

func TestPostgresExecutionClaimLifecycle(t *testing.T) {
	store := newTestPostgres(t)
	testExecutionClaimLifecycle(t, store)

	ctx := context.Background()
	first, err := store.ClaimExecution(ctx, 7, "escrow-1", 13, "owner-a")
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `
UPDATE devshard_execution_claims
SET claimed_at = now() - interval '3 minutes'
WHERE epoch_id = 7 AND escrow_id = 'escrow-1' AND inference_id = 13`)
	require.NoError(t, err)
	second, err := store.ClaimExecution(ctx, 7, "escrow-1", 13, "owner-b")
	require.NoError(t, err)
	require.True(t, second.Acquired)
	require.Greater(t, second.Fence, first.Fence)
	require.ErrorIs(t,
		store.MarkExecutionDispatched(ctx, 7, "escrow-1", 13, "owner-a", first.Fence),
		ErrExecutionClaimNotOwned,
	)
}

func TestExecutionClaimsPrunedWithEpoch(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	_, err := store.ClaimExecution(ctx, 7, "old", 1, "owner")
	require.NoError(t, err)
	_, err = store.ClaimExecution(ctx, 8, "current", 1, "owner")
	require.NoError(t, err)

	require.NoError(t, store.PruneEpoch(7))
	_, err = store.GetExecution(ctx, 7, "old", 1)
	require.True(t, errors.Is(err, ErrExecutionNotFound))
	_, err = store.GetExecution(ctx, 8, "current", 1)
	require.NoError(t, err)
}
