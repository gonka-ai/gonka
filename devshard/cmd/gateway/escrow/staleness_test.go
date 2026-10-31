package escrow

import (
	"context"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

func staleManager(t *testing.T, now time.Time) (*Manager, *fakeStore, *recordingLifecycleNarrator) {
	t.Helper()
	testStore := newFakeStore()
	manager, narrator := deadlineManager(t, testStore, "")
	manager.now = func() time.Time { return now }
	return manager, testStore, narrator
}

func staleSnapshot(height int64, updatedAt time.Time) chain.PhaseSnapshot {
	return chain.PhaseSnapshot{EpochIndex: 8, EffectiveEpochIndex: 8, BlockHeight: height, EpochSwitchBlockHeight: 3100, LastUpdatedAt: updatedAt}
}

// Test flow:
//  1. Measure a tiny block time from a height jump, as a scenario's jumps would.
//  2. Project a snapshot updated thirty seconds ago, within its sixty-second max age.
//  3. Assert the height is unchanged and nothing was narrated.
func TestAFreshSnapshotIsNeverProjected(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	manager, _, narrator := staleManager(t, now)
	manager.pace.observe(1_000, now.Add(-time.Hour))
	manager.pace.observe(2_600, now.Add(-time.Hour+time.Second))

	projected := manager.projectStale(staleSnapshot(2_000, now.Add(-30*time.Second)))

	if projected.BlockHeight != 2_000 || len(narrator.recorded()) != 0 {
		t.Fatalf("projectStale(fresh) height %d, narration %v, want 2000 and none", projected.BlockHeight, narrator.recorded())
	}
}

// Test flow:
//  1. Table-driven: a snapshot ten minutes stale under a measured six-second block, one two minutes stale with no block time measured, and one two minutes stale under a 600-microsecond block a height jump measured.
//  2. Project it twice.
//  3. Assert the height moved by the stale time over the block time (six seconds, or one second when unmeasured or measured under a second) and the projection was narrated once.
func TestAStaleSnapshotReadsDeadlinesAtTheProjectedHeight(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name      string
		blockTime time.Duration
		staleFor  time.Duration
		want      int64
	}{
		{name: "measured six-second blocks", blockTime: 6 * time.Second, staleFor: 10 * time.Minute, want: 2_100},
		{name: "no block time measured", staleFor: 2 * time.Minute, want: 2_120},
		{name: "a block time under a second, measured from a jump", blockTime: 600 * time.Microsecond, staleFor: 2 * time.Minute, want: 2_120},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager, _, narrator := staleManager(t, now)
			if testCase.blockTime > 0 {
				manager.pace.observe(1_000, now.Add(-2*time.Hour))
				manager.pace.observe(1_200, now.Add(-2*time.Hour+200*testCase.blockTime))
			}
			snapshot := staleSnapshot(2_000, now.Add(-testCase.staleFor))

			first := manager.projectStale(snapshot)
			second := manager.projectStale(snapshot)

			if first.BlockHeight != testCase.want || second.BlockHeight != testCase.want {
				t.Fatalf("projectStale() heights %d and %d, want %d", first.BlockHeight, second.BlockHeight, testCase.want)
			}
			if lines := narrator.recorded(); len(lines) != 1 {
				t.Fatalf("narration = %v, want one projection line", lines)
			}
		})
	}
}

// Test flow:
//  1. Store a serving row of chain epoch 7 whose real deadline margin starts at 2500, and a snapshot at 2400 left stale for three minutes with no block time measured.
//  2. Run the deadline pass on the projected snapshot.
//  3. Assert the row was parked: the projection read 2580, inside the margin.
func TestAStaleSnapshotParksARowItsProjectionPutsInsideTheMargin(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	manager, testStore, _ := staleManager(t, now)
	rows := []store.DevshardRecord{{EscrowID: "1", Model: "model-a", Active: true, ChainEpoch: 7}}
	testStore.devshards["1"] = rows[0]

	updated, err := manager.parkAtDeadline(context.Background(), manager.projectStale(staleSnapshot(2_400, now.Add(-3*time.Minute))), rows)

	if err != nil || !slices.ContainsFunc(updated, func(record store.DevshardRecord) bool { return record.EscrowID == "1" && !record.Active }) {
		t.Fatalf("parkAtDeadline(projected) = %+v, %v, want row 1 parked", updated, err)
	}
}
