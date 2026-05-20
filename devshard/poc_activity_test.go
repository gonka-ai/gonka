package devshard

import (
	"testing"

	"devshard/storage"

	"github.com/stretchr/testify/require"
)

func TestPoCActivityTrackerPersistsActivePeriodsByEpoch(t *testing.T) {
	store := storage.NewMemory()
	tracker := NewPoCActivityTracker(false, 0, 0)
	tracker.SetStore(store)

	tracker.Record(true, 100, 7)
	tracker.Record(true, 200, 8)
	tracker.Record(false, 300, 8)

	require.True(t, tracker.WasPoCActiveAt(150))
	require.True(t, tracker.WasPoCActiveAt(250))
	require.False(t, tracker.WasPoCActiveAt(350))

	periods, err := store.ListPoCActivityPeriods()
	require.NoError(t, err)
	require.ElementsMatch(t, []storage.PoCActivityPeriod{
		{EpochID: 7, StartTime: 100, EndTime: 200},
		{EpochID: 8, StartTime: 200, EndTime: 300},
	}, periods)
}

func TestPoCActivityTrackerGraceAllowsSlightlyEarlyTimestamp(t *testing.T) {
	tracker := NewPoCActivityTracker(false, 0, 0)
	tracker.Record(true, 100, 7)

	require.False(t, tracker.WasPoCActiveAt(50))
	require.False(t, tracker.WasPoCActiveAtWithGrace(39, 60))
	require.True(t, tracker.WasPoCActiveAtWithGrace(40, 60))
}

func TestPoCActivityTrackerDetectsActivityWithinWindow(t *testing.T) {
	tracker := NewPoCActivityTracker(false, 0, 0)
	tracker.Record(true, 200, 7)
	tracker.Record(false, 300, 7)

	require.False(t, tracker.WasPoCActiveBetween(100, 199, 0))
	require.True(t, tracker.WasPoCActiveBetween(100, 200, 0))
	require.True(t, tracker.WasPoCActiveBetween(100, 140, 60))
}

func TestPoCActivityTrackerPruneBeforeDropsInMemoryPeriods(t *testing.T) {
	store := storage.NewMemory()
	require.NoError(t, store.RecordPoCActivityPeriod(storage.PoCActivityPeriod{
		EpochID:   7,
		StartTime: 100,
		EndTime:   200,
	}))
	require.NoError(t, store.RecordPoCActivityPeriod(storage.PoCActivityPeriod{
		EpochID:   8,
		StartTime: 200,
		EndTime:   300,
	}))

	tracker := NewPoCActivityTracker(false, 0, 0)
	tracker.SetStore(store)
	require.True(t, tracker.WasPoCActiveAt(150))
	require.True(t, tracker.WasPoCActiveAt(250))

	tracker.PruneBefore(8)
	require.False(t, tracker.WasPoCActiveAt(150))
	require.True(t, tracker.WasPoCActiveAt(250))
}

func TestPoCActivityTrackerPrunesToStoreCursorOnSetStore(t *testing.T) {
	store := &testPrunedCursorStore{Memory: storage.NewMemory(), prunedUpTo: 8}
	require.NoError(t, store.RecordPoCActivityPeriod(storage.PoCActivityPeriod{
		EpochID:   7,
		StartTime: 100,
		EndTime:   200,
	}))
	require.NoError(t, store.RecordPoCActivityPeriod(storage.PoCActivityPeriod{
		EpochID:   8,
		StartTime: 200,
		EndTime:   300,
	}))

	tracker := NewPoCActivityTracker(false, 0, 0)
	tracker.SetStore(store)

	require.False(t, tracker.WasPoCActiveAt(150))
	require.True(t, tracker.WasPoCActiveAt(250))
}
