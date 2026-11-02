package user

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/headertime"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// confirmBelowFloor builds the execution-timeout miss: acks raise F from 100
// to 105, and height 102 is never a running maximum. The header map may still
// hold 102. ExecutionTimeout is 5s.
func confirmBelowFloor(t *testing.T, stamp time.Time, storeConfirm bool) (*Session, *headertime.Source) {
	t.Helper()
	signer := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{signer})
	config := testutil.DefaultConfig(len(group))
	config.ExecutionTimeout = 5
	src := headertime.New()
	hash := []byte{0xaa}
	store := func(height uint64, at time.Time) {
		src.Observe(blocks.HashOnlyHeader(int64(height), at, "mockchain", hash), headertime.OriginDAPI, true)
	}
	store(100, stamp)
	store(105, stamp.Add(time.Second))
	if storeConfirm {
		store(102, stamp)
	}
	sm := statetest.MustStateMachine(t, "escrow-1", config, group, 1_000_000, testutil.MustGenerateKey(t).Address(), signing.NewSecp256k1Verifier(), state.WithHeaderTimes(src))
	raiseRawFloor(t, sm, signer, uint64(len(group)), 100)
	raiseRawFloor(t, sm, signer, uint64(len(group)), 105)

	_, at100 := sm.FloorPointAtHeight(100)
	_, at102 := sm.FloorPointAtHeight(102)
	_, at105 := sm.FloorPointAtHeight(105)
	require.True(t, at100)
	require.False(t, at102, "102 never raised F")
	require.True(t, at105)

	return &Session{sm: sm, escrowID: "escrow-1"}, src
}

func deadlineForConfirm(s *Session, height uint64, now time.Time, outcome *nonceOutcome) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executionDeadlineLocked(7, types.InferenceRecord{
		Status: types.StatusStarted, ConfirmedAtHeight: height,
	}, outcome, now)
}

func TestConfirmedHeightTimeReadsBelowTheFloorMax(t *testing.T) {
	stamp := time.Unix(1_700_000_000, 0).UTC()
	session, _ := confirmBelowFloor(t, stamp, true)

	// The record stores no confirm hash. The hash-checked map read cannot
	// answer, which is why the floor index used to be the only way in.
	_, ok := session.sm.LookupHeaderTimeMs(102, nil)
	require.False(t, ok)
	ms, ok := session.sm.LookupHeaderTimeMs(102, []byte{0xaa})
	require.True(t, ok)
	require.Equal(t, stamp.UnixMilli(), ms)

	session.mu.Lock()
	got, ok := session.confirmedHeightTimeLocked(types.InferenceRecord{ConfirmedAtHeight: 102})
	session.mu.Unlock()
	require.True(t, ok)
	require.Equal(t, stamp.UnixMilli(), got)

	session.mu.Lock()
	_, ok = session.confirmedHeightTimeLocked(types.InferenceRecord{ConfirmedAtHeight: 101})
	session.mu.Unlock()
	require.False(t, ok, "a height the map does not hold stays a miss")
}

func TestExecutionDeadlineRetriesUntilConfirmHeightIsStored(t *testing.T) {
	stamp := time.Unix(1_700_000_000, 0).UTC()
	session, src := confirmBelowFloor(t, stamp, false)
	window := 5 * time.Second

	// The row is not stored yet. Past the timeout, the sweep waits and does
	// not open. It does not fetch.
	now := stamp.Add(window + time.Second)
	outcome := &nonceOutcome{confirmedAt: stamp.Unix()}
	deadline := deadlineForConfirm(session, 102, now, outcome)
	require.Equal(t, now.Add(executionFeedPoll), deadline)
	require.True(t, outcome.feedBehindLogged)

	src.Observe(blocks.HashOnlyHeader(102, stamp, "mockchain", []byte{0xaa}), headertime.OriginDAPI, true)

	// Inside the window the row is visible and the challenge stays closed.
	inside := stamp.Add(time.Second)
	require.Equal(t, stamp.Add(window), deadlineForConfirm(session, 102, inside, outcome))

	// A wall clock exactly on time(height)+ExecutionTimeout is not due.
	edge := stamp.Add(window)
	require.Equal(t, edge.Add(time.Millisecond), deadlineForConfirm(session, 102, edge, outcome))

	// One millisecond later the same row opens the challenge.
	due := edge.Add(time.Millisecond)
	require.Equal(t, due.Add(-time.Millisecond), deadlineForConfirm(session, 102, due, outcome))
}
