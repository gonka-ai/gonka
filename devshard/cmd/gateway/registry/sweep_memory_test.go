package registry

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

func always(uint64) bool { return true }

func voted(sweep *timeoutSweep, due []uint64, budget int) []uint64 {
	sweep.begin(due, sweep.scanSize(budget))
	votes, _ := sweep.choose(due, budget, true, always)
	return votes
}

// Test flow:
//  1. Build a sweep memory and run one tick over due nonces 1 to 4 with a budget of 2, and settle both picked nonces as not applied.
//  2. Run the next tick over the same nonces.
//  3. Assert the second tick picks the two never-voted nonces first, not the two that just failed.
func TestNeverVotedNoncesComeBeforeRetries(t *testing.T) {
	sweep := newTimeoutSweep()
	due := []uint64{1, 2, 3, 4}
	for _, nonce := range voted(sweep, due, 2) {
		sweep.settle(nonce, user.TimeoutResult{})
	}

	require.Equal(t, []uint64{3, 4}, voted(sweep, due, 2))
}

// Test flow:
//  1. Settle one nonce as not applied after its first vote, then run ticks over it alone with a budget of 4.
//  2. Assert it is held back for one tick, picked again on the next, and after a second failure held back for two ticks.
func TestAnInsufficientResultIsNotSelectedUntilItsBackoffHasPassed(t *testing.T) {
	sweep := newTimeoutSweep()
	due := []uint64{9}
	require.Equal(t, []uint64{9}, voted(sweep, due, 4))
	sweep.settle(9, user.TimeoutResult{})

	require.Empty(t, voted(sweep, due, 4), "the tick after a failure is skipped")
	require.Equal(t, []uint64{9}, voted(sweep, due, 4))
	sweep.settle(9, user.TimeoutResult{})
	require.Empty(t, voted(sweep, due, 4))
	require.Empty(t, voted(sweep, due, 4))
	require.Equal(t, []uint64{9}, voted(sweep, due, 4), "the second failure waits two ticks")
}

// Test flow:
//  1. Hold a nonce that has already failed more times than the back-off doubles, and settle it as not applied once more.
//  2. Assert its next vote waits the capped number of ticks instead of a power of two beyond it.
func TestTheBackoffStopsGrowingAtItsCap(t *testing.T) {
	sweep := newTimeoutSweep()
	sweep.records[9] = sweepRecord{failures: sweepBackoffDoublings * 4}

	sweep.settle(9, user.TimeoutResult{})

	require.Equal(t, sweep.tick+1+sweepBackoffCapTicks, sweep.records[9].retryTick)
}

// Test flow:
//  1. Run a tick over three due nonces where the voteable check refuses nonce 2.
//  2. Assert nonce 2 is reported once as unvoteable and not picked, the other two are picked, and a second tick reports nothing new and still skips it.
func TestAnUnvoteableNonceIsSkippedAndReportedOnce(t *testing.T) {
	sweep := newTimeoutSweep()
	due := []uint64{1, 2, 3}
	voteable := func(nonce uint64) bool { return nonce != 2 }

	sweep.begin(due, sweep.scanSize(8))
	votes, unvoteable := sweep.choose(due, 8, true, voteable)
	require.Equal(t, []uint64{1, 3}, votes)
	require.Equal(t, []uint64{2}, unvoteable)

	sweep.begin(due, sweep.scanSize(8))
	votes, unvoteable = sweep.choose(due, 8, true, voteable)
	require.Equal(t, []uint64{1, 3}, votes)
	require.Empty(t, unvoteable)
}

// Test flow:
//  1. Hold back two nonces in the memory, one unvoteable and one failed, so the scan window grows by two.
//  2. Assert the window is the budget plus the two, so the held-back nonces cannot hide a never-voted one from the scan.
func TestTheScanWindowGrowsWithTheNoncesHeldBack(t *testing.T) {
	sweep := newTimeoutSweep()
	sweep.records[1] = sweepRecord{unvoteable: true}
	sweep.records[2] = sweepRecord{failures: 1}

	require.Equal(t, 10, sweep.scanSize(8))
}

// Test flow:
//  1. Hold two nonces in the memory and run a tick whose scan came back shorter than its window, with only one of them still due.
//  2. Assert the memory keeps only the nonce still due, so a closed record does not stay forever.
//  3. Run a tick whose scan filled the window and assert nothing is forgotten, since a full scan cannot show what is missing.
func TestTheMemoryForgetsClosedNoncesOnlyWhenTheScanWasWhole(t *testing.T) {
	sweep := newTimeoutSweep()
	sweep.records[1] = sweepRecord{failures: 1}
	sweep.records[2] = sweepRecord{failures: 1}

	sweep.begin([]uint64{1, 2}, 2)
	require.Len(t, sweep.records, 2)

	sweep.begin([]uint64{1}, 4)
	require.Equal(t, map[uint64]sweepRecord{1: {failures: 1}}, sweep.records)
}

// Test flow:
//  1. Settle a failed nonce as applied, then as skipped for a closed record.
//  2. Assert both clear the nonce from the memory.
func TestAnAppliedOrClosedNonceLeavesTheMemory(t *testing.T) {
	sweep := newTimeoutSweep()
	sweep.records[1] = sweepRecord{failures: 2}
	sweep.records[2] = sweepRecord{failures: 2}

	sweep.settle(1, user.TimeoutResult{Applied: true})
	sweep.settle(2, user.TimeoutResult{Outcome: timeoutOutcomeSkipped})

	require.Empty(t, sweep.records)
}
