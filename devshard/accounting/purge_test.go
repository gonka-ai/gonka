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
