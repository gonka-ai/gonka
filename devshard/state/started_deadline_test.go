package state

import (
	"testing"
	"time"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

const (
	blockTimeNowMs     = int64(10 * 24 * time.Hour / time.Millisecond)
	floorHeightForTest = uint64(100)
)

type fixedBlockTime int64

func (f fixedBlockTime) HeaderTimeMs(uint64, []byte) (int64, error) { return int64(f), nil }

func newMachineAtBlockTime(tb testing.TB, nowMs int64, records map[uint64]*types.InferenceRecord) *StateMachine {
	tb.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(tb), testutil.MustGenerateKey(tb)}
	user := testutil.MustGenerateKey(tb)
	group := testutil.MakeGroup(hosts)
	configuration := testutil.DefaultConfig(len(hosts))
	store := testutil.MustMemoryStore(tb, "escrow-1", user.Address(), configuration, group, 1_000_000)
	machine, err := NewStateMachine("escrow-1", configuration, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), store)
	require.NoError(tb, err)
	machine.headerTimes = fixedBlockTime(nowMs)
	machine.mu.Lock()
	machine.heightSyncFloor.Observe(0, []heightsync.FloorClaim{{Signer: 1, Height: floorHeightForTest, Hash: []byte{0x01}}})
	for id, record := range records {
		machine.state.Inferences[id] = record
	}
	machine.mu.Unlock()
	return machine
}

func startedConfirmedAt(confirmedAtTimeMs int64) *types.InferenceRecord {
	return &types.InferenceRecord{Status: types.StatusStarted, ConfirmedAtTimeMs: confirmedAtTimeMs}
}

func executionWindowMs(machine *StateMachine) int64 {
	return int64(machine.Config().ExecutionTimeout) * time.Second.Milliseconds()
}

// Test flow:
//  1. Build a machine at a known block time with started, pending, finished, timed-out and unstamped records.
//  2. Ask for the started records past their execution deadline.
//  3. Assert only the started record confirmed longer ago than the window comes back.
func TestStartedInferencesPastDeadlineReturnsOnlyWhatTheChainStillSettles(t *testing.T) {
	longPast := blockTimeNowMs - 2*time.Hour.Milliseconds()
	machine := newMachineAtBlockTime(t, blockTimeNowMs, map[uint64]*types.InferenceRecord{
		1: startedConfirmedAt(longPast),
		2: startedConfirmedAt(blockTimeNowMs),
		3: {Status: types.StatusPending, StartedAtTimeMs: longPast},
		4: {Status: types.StatusFinished, ConfirmedAtTimeMs: longPast},
		5: {Status: types.StatusTimedOut, ConfirmedAtTimeMs: longPast},
		6: {Status: types.StatusStarted, StartedAtTimeMs: longPast},
	})

	due := machine.StartedInferencesPastDeadline(0, 0, 0, 10)

	require.Equal(t, []uint64{1}, due,
		"only a started record past its own execution deadline is still settleable and unclaimed, and a record with no confirm time is never armed")
}

// Test flow:
//  1. Build a machine whose block time is unknown, with one record far past its deadline.
//  2. Ask for the started records past their execution deadline.
//  3. Assert nothing comes back, since the deadline cannot be measured.
func TestStartedInferencesPastDeadlineReturnsNothingWithoutBlockTime(t *testing.T) {
	machine := newMachineAtBlockTime(t, blockTimeNowMs, map[uint64]*types.InferenceRecord{1: startedConfirmedAt(1)})
	machine.headerTimes = nil

	require.Nil(t, machine.StartedInferencesPastDeadline(0, 0, 0, 10))
}

// Test flow:
//  1. Build a machine with one record a minute past its execution deadline.
//  2. Ask with no buffer, then with an hour's buffer.
//  3. Assert it is due without the buffer and not due with it.
func TestStartedInferencesPastDeadlineHonoursTheBuffer(t *testing.T) {
	machine := newMachineAtBlockTime(t, blockTimeNowMs, nil)
	pastByAMinute := blockTimeNowMs - executionWindowMs(machine) - time.Minute.Milliseconds()
	machine.mu.Lock()
	machine.state.Inferences[1] = startedConfirmedAt(pastByAMinute)
	machine.mu.Unlock()

	require.Len(t, machine.StartedInferencesPastDeadline(0, 0, 0, 10), 1)
	require.Empty(t, machine.StartedInferencesPastDeadline(time.Hour, 0, 0, 10),
		"the buffer keeps the sweep off a nonce the race that owns it is still due to vote")
}

// Test flow:
//  1. Build a machine with records 4 and 5 a minute past their execution deadline.
//  2. Ask with an hour's race grace and racesFrom 5.
//  3. Assert record 4, below racesFrom, is due and record 5 waits out the grace.
func TestStartedInferencesPastDeadlineWaitsOutTheRaceGraceOnlyFromRacesFrom(t *testing.T) {
	machine := newMachineAtBlockTime(t, blockTimeNowMs, nil)
	pastByAMinute := blockTimeNowMs - executionWindowMs(machine) - time.Minute.Milliseconds()
	machine.mu.Lock()
	machine.state.Inferences[4] = startedConfirmedAt(pastByAMinute)
	machine.state.Inferences[5] = startedConfirmedAt(pastByAMinute)
	machine.mu.Unlock()

	due := machine.StartedInferencesPastDeadline(0, time.Hour, 5, 10)

	require.Equal(t, []uint64{4}, due, "a record below racesFrom has no race left to wait for; one from it on waits out the race grace")
}

// Test flow:
//  1. Build a machine with 50 overdue records, each a minute less overdue than the one before.
//  2. Ask for four of them.
//  3. Assert the four overdue longest come back, oldest first.
func TestStartedInferencesPastDeadlineReturnsTheMostOverdueFirst(t *testing.T) {
	records := make(map[uint64]*types.InferenceRecord, 50)
	for id := uint64(1); id <= 50; id++ {
		records[id] = startedConfirmedAt(blockTimeNowMs - 2*time.Hour.Milliseconds() + int64(id)*time.Minute.Milliseconds())
	}
	machine := newMachineAtBlockTime(t, blockTimeNowMs, records)

	due := machine.StartedInferencesPastDeadline(0, 0, 0, 4)

	require.Equal(t, []uint64{1, 2, 3, 4}, due, "a budget smaller than the backlog goes to the records overdue longest, oldest first")
}

// Test flow:
//  1. Build a machine with 50 records equally overdue.
//  2. Ask with a budget of four, then of zero.
//  3. Assert four ids come back, then nil.
func TestStartedInferencesPastDeadlineStopsAtTheBudget(t *testing.T) {
	records := make(map[uint64]*types.InferenceRecord, 50)
	for id := uint64(1); id <= 50; id++ {
		records[id] = startedConfirmedAt(blockTimeNowMs - 2*time.Hour.Milliseconds())
	}
	machine := newMachineAtBlockTime(t, blockTimeNowMs, records)

	require.Len(t, machine.StartedInferencesPastDeadline(0, 0, 0, 4), 4,
		"the budget is what bounds the work, whatever the backlog is")
	require.Nil(t, machine.StartedInferencesPastDeadline(0, 0, 0, 0),
		"a zero budget scans nothing and allocates nothing")
}
