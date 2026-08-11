package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func testExecutionClaimLifecycle(t *testing.T, store ExecutionStore) {
	t.Helper()
	ctx := context.Background()

	first, err := store.ClaimExecution(ctx, 7, "escrow-1", 11, "owner-a")
	require.NoError(t, err)
	require.True(t, first.Acquired)
	require.Equal(t, ExecutionPending, first.Status)
	require.NotZero(t, first.Fence)

	second, err := store.ClaimExecution(ctx, 7, "escrow-1", 11, "owner-b")
	require.NoError(t, err)
	require.False(t, second.Acquired)
	require.Equal(t, first.Fence, second.Fence)
	require.Equal(t, ExecutionPending, second.Status)

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
}

func TestMemoryExecutionClaimLifecycle(t *testing.T) {
	testExecutionClaimLifecycle(t, NewMemory())
}

func TestPostgresExecutionClaimLifecycle(t *testing.T) {
	testExecutionClaimLifecycle(t, newTestPostgres(t))
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
