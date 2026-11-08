package accounting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An operator discarding one bad epoch must not lose the epochs either side of it.
func TestPurgeEpoch_TakesOnlyTheEpochNamed(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "before", 8, "m")
	registerEscrow(t, tracker, "target", 9, "m")
	registerEscrow(t, tracker, "after", 10, "m")

	removed, err := tracker.PurgeEpoch(context.Background(), 9)

	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.Empty(t, tracker.Query(QueryFilter{EpochIndex: 9}))
	require.NotEmpty(t, tracker.Query(QueryFilter{EpochIndex: 8}))
	require.NotEmpty(t, tracker.Query(QueryFilter{EpochIndex: 10}))
}

// Epoch 0 is what an absent field decodes to, so accepting it would turn a malformed request into a
// ledger-wide wipe.
func TestPurgeEpoch_RefusesEpochZero(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m")

	removed, err := tracker.PurgeEpoch(context.Background(), 0)

	require.ErrorIs(t, err, ErrPurgeEpochRequired)
	require.Zero(t, removed)
	require.NotEmpty(t, tracker.Query(QueryFilter{EpochIndex: 9}), "nothing was discarded")
}

func TestPurgeEpoch_AnEpochWithNothingInItIsNotAnError(t *testing.T) {
	tracker := newTestTracker(t)
	registerEscrow(t, tracker, "e1", 9, "m")

	removed, err := tracker.PurgeEpoch(context.Background(), 42)

	require.NoError(t, err)
	require.Zero(t, removed)
}

// The snapshot tick is the only other writer, so a purge that is not flushed is lost to a crash.
// Closing the tracker would flush anyway, which is why this reads the file while it is still open:
// otherwise the assertion passes on Close's write and says nothing about the purge itself.
func TestPurgeEpoch_ReachesDiskWithoutWaitingForClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	live, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	live.now = func() time.Time { return accountingTestNow }
	t.Cleanup(func() { require.NoError(t, live.Close()) })
	registerEscrow(t, live, "e1", 9, "m")
	require.NoError(t, live.RecordDiff("e1", 1, true))
	require.NoError(t, live.Flush(context.Background()))

	reloadedBefore, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, reloadedBefore.Query(QueryFilter{EpochIndex: 9}), "precondition: the epoch is on disk")
	require.NoError(t, reloadedBefore.Close())

	removed, err := live.PurgeEpoch(context.Background(), 9)
	require.NoError(t, err)
	require.Equal(t, 1, removed)

	reloadedAfter, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reloadedAfter.Close()) })
	require.Empty(t, reloadedAfter.Query(QueryFilter{EpochIndex: 9}), "the purge never reached disk")
}

// Postgres deletes only the ids the snapshot names. A purge that only drops
// the in-memory escrow leaves the row, and the next reopen loads it back.
func TestPurgeEpoch_NamesTheDeletedEscrow(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "keep", 8, "m")
	registerEscrow(t, tr, "gone", 9, "m")
	require.NoError(t, tr.RecordDiff("keep", 1, true))
	require.NoError(t, tr.RecordDiff("gone", 2, true))

	backend := &failingBackend{}
	tr.store = &Store{backend: backend}

	removed, err := tr.PurgeEpoch(context.Background(), 9)

	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.Equal(t, []string{"gone"}, backend.lastDeleted)
	require.Equal(t, []string{"keep"}, backend.lastDirty)
	require.Equal(t, []uint64{9}, backend.lastPurged)
	require.Empty(t, tr.Query(QueryFilter{EpochIndex: 9}))
	require.NotEmpty(t, tr.Query(QueryFilter{EpochIndex: 8}))
}

// A rejected flush must not forget the delete. The next snapshot is the retry.
func TestPurgeEpoch_RetriesDeleteAfterSaveFails(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "gone", 9, "m")
	require.NoError(t, tr.RecordDiff("gone", 1, true))

	backend := &failingBackend{fail: true}
	tr.store = &Store{backend: backend}

	_, err := tr.PurgeEpoch(context.Background(), 9)
	require.Error(t, err)
	require.Equal(t, []string{"gone"}, backend.lastDeleted)
	require.Equal(t, []uint64{9}, backend.lastPurged)

	backend.fail = false
	require.NoError(t, tr.Flush(context.Background()))
	require.Equal(t, []string{"gone"}, backend.lastDeleted)
	require.Equal(t, []uint64{9}, backend.lastPurged)
}

// The first call already dropped the escrow and the flush failed, so a retry
// finds nothing to remove. The delete is still pending and has to be sent.
func TestPurgeEpoch_RetryFlushesADeleteThatAlreadyLeftMemory(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "gone", 9, "m")
	require.NoError(t, tr.RecordDiff("gone", 1, true))

	backend := &failingBackend{fail: true}
	tr.store = &Store{backend: backend}
	_, err := tr.PurgeEpoch(context.Background(), 9)
	require.Error(t, err)

	backend.fail = false
	backend.lastDeleted = nil
	backend.lastPurged = nil
	removed, err := tr.PurgeEpoch(context.Background(), 9)
	require.NoError(t, err)
	require.Zero(t, removed)
	require.Equal(t, []string{"gone"}, backend.lastDeleted)
	require.Equal(t, []uint64{9}, backend.lastPurged)
}

// An epoch this tracker never recorded is not an error and must not flush
// unrelated dirty escrows.
func TestPurgeEpoch_EmptyEpochDoesNotFlush(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "keep", 8, "m")
	backend := &failingBackend{}
	tr.store = &Store{backend: backend}

	removed, err := tr.PurgeEpoch(context.Background(), 42)

	require.NoError(t, err)
	require.Zero(t, removed)
	require.Nil(t, backend.lastDirty)
	require.Nil(t, backend.lastDeleted)
}

func TestRegisterEscrow_ClearsPendingDelete(t *testing.T) {
	tr := newModeTestTracker()
	tr.pendingDeletes = map[string]struct{}{"e1": {}, "other": {}}

	registerEscrow(t, tr, "e1", 9, "m")

	_, pending := tr.pendingDeletes["e1"]
	require.False(t, pending)
	_, other := tr.pendingDeletes["other"]
	require.True(t, other)
}

func TestRestorePersistState_SkipsEscrowThatCameBack(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "back", 9, "m")

	tr.restorePersistState([]string{"back", "gone"}, []string{"back", "gone"}, []uint64{9})

	_, back := tr.pendingDeletes["back"]
	require.False(t, back)
	_, gone := tr.pendingDeletes["gone"]
	require.True(t, gone)
	_, dirty := tr.dirty["back"]
	require.True(t, dirty)
}

// The failed purge armed the delete. Registering the epoch again is refused,
// and the delete stays armed for the retry.
func TestPurgeEpoch_ReregisterAfterFailedFlushIsNotDeleted(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "gone", 9, "m")
	meta := tr.escrows["gone"].Meta
	backend := &failingBackend{fail: true}
	tr.store = &Store{backend: backend}
	_, err := tr.PurgeEpoch(context.Background(), 9)
	require.Error(t, err)

	require.ErrorIs(t, tr.RegisterEscrow(meta), ErrEpochPurged)
	backend.fail = false
	require.NoError(t, tr.Flush(context.Background()))
	require.Equal(t, []string{"gone"}, backend.lastDeleted)
	require.NotContains(t, backend.lastDirty, "gone")
	require.Equal(t, []uint64{9}, backend.lastPurged)
}

func TestPurgeEpoch_RecordingAPurgedEscrowIsQuiet(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "gone", 9, "m")
	registerEscrow(t, tr, "keep", 8, "m")
	_, err := tr.PurgeEpoch(context.Background(), 9)
	require.NoError(t, err)

	require.NoError(t, tr.RecordDiff("gone", 4, true))
	recording, _ := tr.ErrorCounts()
	require.Zero(t, recording)
	require.Empty(t, tr.Query(QueryFilter{EpochIndex: 9}))
	require.NotEmpty(t, tr.Query(QueryFilter{EpochIndex: 8}))

	require.Error(t, tr.RecordDiff("missing", 1, true))
	recording, _ = tr.ErrorCounts()
	require.Equal(t, uint64(1), recording)
}

func TestPurgeEpoch_RegisterOfPurgedEpochIsRefused(t *testing.T) {
	tr := newModeTestTracker()
	registerEscrow(t, tr, "gone", 9, "m")
	meta := tr.escrows["gone"].Meta
	_, err := tr.PurgeEpoch(context.Background(), 9)
	require.NoError(t, err)

	require.ErrorIs(t, tr.RegisterEscrow(meta), ErrEpochPurged)
	require.False(t, tr.RetainsEscrow("gone"))
	registerEscrow(t, tr, "later", 10, "m")
	require.True(t, tr.RetainsEscrow("later"))
	recording, _ := tr.ErrorCounts()
	require.Zero(t, recording, "refusing a purged epoch is not a recorder failure")
}
