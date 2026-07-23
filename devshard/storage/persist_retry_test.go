package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

type flakyAppendStore struct {
	Storage
	failsLeft atomic.Int32
	calls     atomic.Int32
}

func (s *flakyAppendStore) AppendDiff(escrowID string, rec types.DiffRecord) error {
	s.calls.Add(1)
	if s.failsLeft.Add(-1) >= 0 {
		return errors.New("transient append failure")
	}
	return s.Storage.AppendDiff(escrowID, rec)
}

func TestAppendDiffWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	inner := NewMemory()
	require.NoError(t, inner.CreateSession(defaultParams()))
	store := &flakyAppendStore{Storage: inner}
	store.failsLeft.Store(2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, AppendDiffWithRetry(ctx, store, "escrow-1", makeDiffRecord(1)))
	require.Equal(t, int32(3), store.calls.Load())
	diffs, err := inner.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}

func TestAppendDiffWithRetry_Exhausted(t *testing.T) {
	inner := NewMemory()
	require.NoError(t, inner.CreateSession(defaultParams()))
	store := &flakyAppendStore{Storage: inner}
	store.failsLeft.Store(100)

	err := AppendDiffWithRetry(context.Background(), store, "escrow-1", makeDiffRecord(1))
	require.ErrorIs(t, err, ErrPersistExhausted)
	require.Equal(t, int32(DefaultPersistMaxAttempts), store.calls.Load())
}

func TestAppendDiffWithRetry_ForkNoRetry(t *testing.T) {
	inner := NewMemory()
	require.NoError(t, inner.CreateSession(defaultParams()))
	require.NoError(t, inner.AppendDiff("escrow-1", makeDiffRecord(1)))

	conflict := makeDiffRecord(1)
	conflict.StateHash = []byte("other")
	calls := 0
	store := &countingAppendOnce{Storage: inner, onAppend: func() { calls++ }}
	err := AppendDiffWithRetry(context.Background(), store, "escrow-1", conflict)
	require.ErrorIs(t, err, ErrDiffFork)
	require.Equal(t, 1, calls, "fork must not retry")
}

type countingAppendOnce struct {
	Storage
	onAppend func()
}

func (s *countingAppendOnce) AppendDiff(escrowID string, rec types.DiffRecord) error {
	if s.onAppend != nil {
		s.onAppend()
	}
	return s.Storage.AppendDiff(escrowID, rec)
}

func TestAppendDiffWithRetry_ContextCancel(t *testing.T) {
	inner := NewMemory()
	require.NoError(t, inner.CreateSession(defaultParams()))
	store := &flakyAppendStore{Storage: inner}
	store.failsLeft.Store(100)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := AppendDiffWithRetry(ctx, store, "escrow-1", makeDiffRecord(1))
	require.ErrorIs(t, err, context.Canceled)
}

var _ Storage = (*flakyAppendStore)(nil)
