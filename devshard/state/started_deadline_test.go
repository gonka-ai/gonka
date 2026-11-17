package state

import (
	"testing"
	"time"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func machineWithStartedRecords(t *testing.T, records map[uint64]*types.InferenceRecord) *StateMachine {
	t.Helper()
	machine, _ := newTestSM(t, []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}, 1_000_000)
	machine.mu.Lock()
	for id, record := range records {
		machine.state.Inferences[id] = record
	}
	machine.mu.Unlock()
	return machine
}

func TestStartedInferencesPastDeadlineReturnsOnlyWhatTheChainStillSettles(t *testing.T) {
	now := time.Now()
	longPast := now.Add(-2 * time.Hour).Unix()
	justNow := now.Unix()

	machine := machineWithStartedRecords(t, map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusStarted, ConfirmedAt: longPast},
		2: {Status: types.StatusStarted, ConfirmedAt: justNow},
		3: {Status: types.StatusPending, ConfirmedAt: longPast},
		4: {Status: types.StatusFinished, ConfirmedAt: longPast},
		5: {Status: types.StatusTimedOut, ConfirmedAt: longPast},
		6: {Status: types.StatusStarted, StartedAt: justNow},
	})

	due := machine.StartedInferencesPastDeadline(now, 0, 0, 0, 10)

	require.Equal(t, []uint64{1}, due,
		"only a started record past its own execution deadline is still settleable and unclaimed")
}

// An executor that stamps no confirm time leaves a record verifiers already time out; the sweep anchors it on its start.
func TestStartedInferencesPastDeadlineAnchorsAnUnstampedRecordOnItsStart(t *testing.T) {
	now := time.Now()
	machine := machineWithStartedRecords(t, map[uint64]*types.InferenceRecord{})
	executionTimeout := time.Duration(machine.Config().ExecutionTimeout) * time.Second
	machine.mu.Lock()
	machine.state.Inferences[1] = &types.InferenceRecord{Status: types.StatusStarted, StartedAt: now.Add(-executionTimeout - time.Minute).Unix()}
	machine.state.Inferences[2] = &types.InferenceRecord{Status: types.StatusStarted, StartedAt: now.Unix()}
	machine.mu.Unlock()

	due := machine.StartedInferencesPastDeadline(now, 0, 0, 0, 10)

	require.Equal(t, []uint64{1}, due, "an unstamped record is due once its start is past the execution deadline, and not before")
}

func TestStartedInferencesPastDeadlineHonoursTheGrace(t *testing.T) {
	now := time.Now()
	machine := machineWithStartedRecords(t, map[uint64]*types.InferenceRecord{})
	executionTimeout := time.Duration(machine.Config().ExecutionTimeout) * time.Second
	machine.mu.Lock()
	machine.state.Inferences[1] = &types.InferenceRecord{
		Status: types.StatusStarted, ConfirmedAt: now.Add(-executionTimeout - time.Minute).Unix(),
	}
	machine.mu.Unlock()

	require.Len(t, machine.StartedInferencesPastDeadline(now, 0, 0, 0, 10), 1)
	require.Empty(t, machine.StartedInferencesPastDeadline(now, time.Hour, 0, 0, 10),
		"the grace keeps the sweep off a nonce the race that owns it is still due to vote")
}

func TestStartedInferencesPastDeadlineWaitsOutTheRaceGraceOnlyFromRacesFrom(t *testing.T) {
	now := time.Now()
	machine := machineWithStartedRecords(t, map[uint64]*types.InferenceRecord{})
	executionTimeout := time.Duration(machine.Config().ExecutionTimeout) * time.Second
	pastDeadline := now.Add(-executionTimeout - time.Minute).Unix()
	machine.mu.Lock()
	machine.state.Inferences[4] = &types.InferenceRecord{Status: types.StatusStarted, ConfirmedAt: pastDeadline}
	machine.state.Inferences[5] = &types.InferenceRecord{Status: types.StatusStarted, ConfirmedAt: pastDeadline}
	machine.mu.Unlock()

	due := machine.StartedInferencesPastDeadline(now, 0, time.Hour, 5, 10)

	require.Equal(t, []uint64{4}, due, "a record below racesFrom has no race left to wait for; one from it on waits out the race grace")
}

func TestStartedInferencesPastDeadlineReturnsTheMostOverdueFirst(t *testing.T) {
	now := time.Now()
	records := make(map[uint64]*types.InferenceRecord, 50)
	for id := uint64(1); id <= 50; id++ {
		records[id] = &types.InferenceRecord{Status: types.StatusStarted, ConfirmedAt: now.Add(-2*time.Hour + time.Duration(id)*time.Minute).Unix()}
	}
	machine := machineWithStartedRecords(t, records)

	due := machine.StartedInferencesPastDeadline(now, 0, 0, 0, 4)

	require.Equal(t, []uint64{1, 2, 3, 4}, due, "a budget smaller than the backlog goes to the records overdue longest, oldest first")
}

func TestStartedInferencesPastDeadlineStopsAtTheBudget(t *testing.T) {
	now := time.Now()
	confirmedAt := now.Add(-2 * time.Hour).Unix()

	records := make(map[uint64]*types.InferenceRecord, 50)
	for id := uint64(1); id <= 50; id++ {
		records[id] = &types.InferenceRecord{Status: types.StatusStarted, ConfirmedAt: confirmedAt}
	}
	machine := machineWithStartedRecords(t, records)

	require.Len(t, machine.StartedInferencesPastDeadline(now, 0, 0, 0, 4), 4,
		"the budget is what bounds the work, whatever the backlog is")
	require.Nil(t, machine.StartedInferencesPastDeadline(now, 0, 0, 0, 0),
		"a zero budget scans nothing and allocates nothing")
}
