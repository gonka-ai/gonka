package storage

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/observability"
	"devshard/types"
)

func runAppendDiffIdempotentReplay(t *testing.T, store Storage) {
	t.Helper()
	require.NoError(t, store.CreateSession(defaultParams()))

	rec := makeDiffRecord(1)
	require.NoError(t, store.AppendDiff("escrow-1", rec))
	require.NoError(t, store.AppendDiff("escrow-1", rec), "identical replay must succeed")

	diffs, err := store.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, uint64(1), diffs[0].Nonce)
	require.Equal(t, rec.StateHash, diffs[0].StateHash)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
}

func runAppendDiffForkConflict(t *testing.T, store Storage) {
	t.Helper()
	require.NoError(t, store.CreateSession(defaultParams()))
	require.NoError(t, store.AppendDiff("escrow-1", makeDiffRecord(1)))

	before := testutil.ToFloat64(observability.DiffForkDetectedForTest("escrow-1"))

	conflict := makeDiffRecord(1)
	conflict.StateHash = []byte("different-hash")
	err := store.AppendDiff("escrow-1", conflict)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDiffFork, "got %v", err)

	after := testutil.ToFloat64(observability.DiffForkDetectedForTest("escrow-1"))
	require.Equal(t, before+1, after, "diff_fork_detected must increment")

	diffs, err := store.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, []byte{1}, diffs[0].StateHash, "original row must stay intact")
}

func runAppendDiffMonotonicUnchanged(t *testing.T, store Storage) {
	t.Helper()
	require.NoError(t, store.CreateSession(defaultParams()))
	require.NoError(t, store.AppendDiff("escrow-1", makeDiffRecord(1)))
	require.NoError(t, store.AppendDiff("escrow-1", makeDiffRecord(2)))
	require.NoError(t, store.AppendDiff("escrow-1", makeDiffRecord(3)))

	diffs, err := store.GetDiffs("escrow-1", 1, 3)
	require.NoError(t, err)
	require.Len(t, diffs, 3)
	for i, d := range diffs {
		require.Equal(t, uint64(i+1), d.Nonce)
	}
}

func TestMemory_AppendDiff_IdempotentReplay(t *testing.T) {
	runAppendDiffIdempotentReplay(t, NewMemory())
}

func TestMemory_AppendDiff_ForkConflict(t *testing.T) {
	runAppendDiffForkConflict(t, NewMemory())
}

func TestMemory_AppendDiff_MonotonicUnchanged(t *testing.T) {
	runAppendDiffMonotonicUnchanged(t, NewMemory())
}

func TestSQLite_AppendDiff_IdempotentReplay(t *testing.T) {
	runAppendDiffIdempotentReplay(t, newTestSQLite(t))
}

func TestSQLite_AppendDiff_ForkConflict(t *testing.T) {
	runAppendDiffForkConflict(t, newTestSQLite(t))
}

func TestSQLite_AppendDiff_MonotonicUnchanged(t *testing.T) {
	runAppendDiffMonotonicUnchanged(t, newTestSQLite(t))
}

func TestPostgres_AppendDiff_IdempotentReplay(t *testing.T) {
	runAppendDiffIdempotentReplay(t, newTestPostgres(t))
}

func TestPostgres_AppendDiff_ForkConflict(t *testing.T) {
	runAppendDiffForkConflict(t, newTestPostgres(t))
}

func TestPostgres_AppendDiff_MonotonicUnchanged(t *testing.T) {
	runAppendDiffMonotonicUnchanged(t, newTestPostgres(t))
}

// Ensure makeDiffRecord warm-key / txs empty paths still fork on UserSig.
func TestMemory_AppendDiff_ForkOnUserSig(t *testing.T) {
	store := NewMemory()
	require.NoError(t, store.CreateSession(defaultParams()))
	require.NoError(t, store.AppendDiff("escrow-1", makeDiffRecord(1)))

	conflict := types.DiffRecord{
		Diff: types.Diff{
			Nonce:   1,
			UserSig: []byte("other-sig"),
		},
		StateHash:  []byte{1},
		Signatures: map[uint32][]byte{0: {1}},
	}
	err := store.AppendDiff("escrow-1", conflict)
	require.ErrorIs(t, err, ErrDiffFork)
}
