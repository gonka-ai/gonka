package devshard

import (
	"testing"

	"devshard/storage"

	"github.com/stretchr/testify/require"
)

func TestAvailabilityTrackerPersistsDisabledPeriodsByEpoch(t *testing.T) {
	store := storage.NewMemory()
	tracker := NewAvailabilityTracker(true, 0, 0)
	tracker.SetStore(store)

	tracker.Record(false, 100, 7)
	tracker.Record(false, 200, 8)
	tracker.Record(true, 300, 8)

	require.True(t, tracker.WasUnavailableAt(150))
	require.True(t, tracker.WasUnavailableAt(250))
	require.False(t, tracker.WasUnavailableAt(350))

	periods, err := store.ListAvailabilityPeriods()
	require.NoError(t, err)
	require.ElementsMatch(t, []storage.AvailabilityPeriod{
		{EpochID: 7, StartTime: 100, EndTime: 200},
		{EpochID: 8, StartTime: 200, EndTime: 300},
	}, periods)
}

func TestAvailabilityTrackerGraceAllowsSlightlyEarlyTimestamp(t *testing.T) {
	tracker := NewAvailabilityTracker(true, 0, 0)
	tracker.Record(false, 100, 7)

	require.False(t, tracker.WasUnavailableAt(50))
	require.False(t, tracker.WasUnavailableAtWithGrace(39, 60))
	require.True(t, tracker.WasUnavailableAtWithGrace(40, 60))
}

func TestAvailabilityTrackerClosesPersistedOpenPeriodAfterRestart(t *testing.T) {
	store := storage.NewMemory()
	require.NoError(t, store.RecordAvailabilityPeriod(storage.AvailabilityPeriod{
		EpochID:   7,
		StartTime: 100,
		EndTime:   0,
	}))

	tracker := NewAvailabilityTracker(true, 0, 0)
	tracker.SetStore(store)
	tracker.Record(true, 200, 8)

	periods, err := store.ListAvailabilityPeriods()
	require.NoError(t, err)
	require.ElementsMatch(t, []storage.AvailabilityPeriod{
		{EpochID: 7, StartTime: 100, EndTime: 200},
	}, periods)
	require.True(t, tracker.WasUnavailableAt(150))
	require.False(t, tracker.WasUnavailableAt(250))
}

func TestAvailabilityTrackerPruneBeforeDropsInMemoryPeriods(t *testing.T) {
	store := storage.NewMemory()
	require.NoError(t, store.RecordAvailabilityPeriod(storage.AvailabilityPeriod{
		EpochID:   7,
		StartTime: 100,
		EndTime:   200,
	}))
	require.NoError(t, store.RecordAvailabilityPeriod(storage.AvailabilityPeriod{
		EpochID:   8,
		StartTime: 200,
		EndTime:   300,
	}))

	tracker := NewAvailabilityTracker(true, 0, 0)
	tracker.SetStore(store)
	require.True(t, tracker.WasUnavailableAt(150))
	require.True(t, tracker.WasUnavailableAt(250))

	tracker.PruneBefore(8)
	require.False(t, tracker.WasUnavailableAt(150))
	require.True(t, tracker.WasUnavailableAt(250))
}

type testPrunedCursorStore struct {
	*storage.Memory
	prunedUpTo uint64
}

func (s *testPrunedCursorStore) PrunedUpTo() uint64 {
	return s.prunedUpTo
}

func TestAvailabilityTrackerPrunesToStoreCursorOnSetStore(t *testing.T) {
	store := &testPrunedCursorStore{Memory: storage.NewMemory(), prunedUpTo: 8}
	require.NoError(t, store.RecordAvailabilityPeriod(storage.AvailabilityPeriod{
		EpochID:   7,
		StartTime: 100,
		EndTime:   200,
	}))
	require.NoError(t, store.RecordAvailabilityPeriod(storage.AvailabilityPeriod{
		EpochID:   8,
		StartTime: 200,
		EndTime:   300,
	}))

	tracker := NewAvailabilityTracker(true, 0, 0)
	tracker.SetStore(store)

	require.False(t, tracker.WasUnavailableAt(150))
	require.True(t, tracker.WasUnavailableAt(250))
}
