package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"devshard/types"
)

func batchRecs(from, to uint64) []types.DiffRecord {
	recs := make([]types.DiffRecord, 0, to-from+1)
	for n := from; n <= to; n++ {
		recs = append(recs, types.DiffRecord{
			Diff:       types.Diff{Nonce: n, UserSig: []byte(fmt.Sprintf("sig-%d", n)), PostStateRoot: []byte{byte(n)}},
			StateHash:  []byte{byte(n), 0xAA},
			Signatures: map[uint32][]byte{0: []byte(fmt.Sprintf("host-%d", n))},
		})
	}
	return recs
}

// scriptedBatchStore fails AppendDiffBatch with the scripted errors in order,
// then writes through to Memory.
type scriptedBatchStore struct {
	*Memory
	errs  []error
	calls atomic.Int32
	// block, when set, makes each call wait for ctx like a pool with no free
	// connection.
	block bool
}

func (s *scriptedBatchStore) AppendDiffBatch(ctx context.Context, escrowID string, recs []types.DiffRecord) error {
	n := int(s.calls.Add(1))
	if s.block {
		<-ctx.Done()
		return fmt.Errorf("%w: %w", ErrConnWait, ctx.Err())
	}
	if n <= len(s.errs) && s.errs[n-1] != nil {
		return s.errs[n-1]
	}
	return s.Memory.AppendDiffBatch(ctx, escrowID, recs)
}

func newBatchMemory(t *testing.T) *Memory {
	t.Helper()
	m := NewMemory()
	require.NoError(t, m.CreateSession(defaultParams()))
	return m
}

func TestPersistDiffBatch_RetriesTransientThenCommitsOnce(t *testing.T) {
	m := newBatchMemory(t)
	store := &scriptedBatchStore{Memory: m, errs: []error{
		&pgconn.PgError{Code: "40001"},
		fmt.Errorf("commit: %w", io.ErrUnexpectedEOF),
	}}

	recs := batchRecs(1, 5)
	require.NoError(t, PersistDiffBatch(context.Background(), store, "escrow-1", recs))
	require.Equal(t, int32(3), store.calls.Load())

	got, err := m.GetDiffs("escrow-1", 1, 5)
	require.NoError(t, err)
	require.Len(t, got, 5, "each nonce is stored exactly once")
}

func TestPersistDiffBatch_PoolFullUntilDeadlineIsStorageBusy(t *testing.T) {
	m := newBatchMemory(t)
	store := &scriptedBatchStore{Memory: m, block: true}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := PersistDiffBatch(ctx, store, "escrow-1", batchRecs(1, 3))
	require.ErrorIs(t, err, ErrStorageBusy)
	require.Less(t, time.Since(start), 2*time.Second, "the wait is bounded by the caller's deadline")

	got, err := m.GetDiffs("escrow-1", 1, 3)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestPersistDiffBatch_CallerCancelIsNotBusy(t *testing.T) {
	m := newBatchMemory(t)
	store := &scriptedBatchStore{Memory: m, block: true}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	err := PersistDiffBatch(ctx, store, "escrow-1", batchRecs(1, 3))
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrStorageBusy)
}

func TestPersistDiffBatch_ForkIsNotRetried(t *testing.T) {
	m := newBatchMemory(t)
	store := &scriptedBatchStore{Memory: m, errs: []error{fmt.Errorf("%w: nonce 2", ErrDiffFork)}}
	err := PersistDiffBatch(context.Background(), store, "escrow-1", batchRecs(1, 3))
	require.ErrorIs(t, err, ErrDiffFork)
	require.Equal(t, int32(1), store.calls.Load())
}

func TestPersistDiffBatch_UnclassifiedFailuresExhaust(t *testing.T) {
	m := newBatchMemory(t)
	errs := make([]error, DefaultPersistMaxAttempts)
	for i := range errs {
		errs[i] = errors.New("boom")
	}
	store := &scriptedBatchStore{Memory: m, errs: errs}
	err := PersistDiffBatch(context.Background(), store, "escrow-1", batchRecs(1, 2))
	require.ErrorIs(t, err, ErrPersistExhausted)
	require.Equal(t, int32(DefaultPersistMaxAttempts), store.calls.Load())
}

func TestMemoryAppendDiffBatch_ForkLeavesSessionUnchanged(t *testing.T) {
	m := newBatchMemory(t)
	require.NoError(t, m.AppendDiffBatch(context.Background(), "escrow-1", batchRecs(1, 2)))

	forked := batchRecs(2, 4)
	forked[0].UserSig = []byte("other")
	err := m.AppendDiffBatch(context.Background(), "escrow-1", forked)
	require.ErrorIs(t, err, ErrDiffFork)

	got, err := m.GetDiffs("escrow-1", 1, 4)
	require.NoError(t, err)
	require.Len(t, got, 2, "a fork anywhere in the batch writes none of it")

	require.NoError(t, m.AppendDiffBatch(context.Background(), "escrow-1", batchRecs(1, 4)),
		"an identical replay of a durable prefix is a no-op")
	got, err = m.GetDiffs("escrow-1", 1, 4)
	require.NoError(t, err)
	require.Len(t, got, 4)
}

func TestIsTransientStorageErr(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"conn wait", fmt.Errorf("%w: %w", ErrConnWait, context.DeadlineExceeded), true},
		{"serialization", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, true},
		{"statement timeout", &pgconn.PgError{Code: "57014"}, true},
		{"connection failure", &pgconn.PgError{Code: "08006"}, true},
		{"too many connections", &pgconn.PgError{Code: "53300"}, true},
		{"unexpected eof", fmt.Errorf("x: %w", io.ErrUnexpectedEOF), true},
		{"sqlite busy", errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"fork", ErrDiffFork, false},
		{"plain", errors.New("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsTransientStorageErr(tc.err))
		})
	}
}
