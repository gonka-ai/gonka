package registry

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Test flow:
//  1. Build a registry with one escrow session reporting a LatestNonce of 7, and a Retiring hook that records the session's close-call count and the nonce it reads at the moment it fires.
//  2. Add and then retire that escrow.
//  3. Assert the hook read nonce 7, saw zero close calls at read time, and the session's close-call count is 1 afterward.
func TestARetiringEscrowIsReadBeforeItsSessionCloses(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.escrowState = types.EscrowState{LatestNonce: 7}
	var closedWhenRead int64
	var readNonce uint64
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Retiring: func(_ string, retiring EscrowSession) {
			closedWhenRead = session.closeCalls.Load()
			readNonce = retiring.SnapshotState().LatestNonce
		},
		Now: fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	require.NoError(t, registry.Retire("1"))

	require.Equal(t, uint64(7), readNonce, "the retiring escrow's own session never reached the observer")
	require.Zero(t, closedWhenRead, "the session was already closed when the observer read it")
	require.Equal(t, int64(1), session.closeCalls.Load())
}

// Test flow:
//  1. Build a registry with one escrow session reporting LatestNonce 7, and a Retiring hook that appends each read nonce to a slice.
//  2. Add the escrow and acquire it so a request is in flight.
//  3. Retire the escrow and assert nothing was read yet, since the request is still spending nonces on it.
//  4. Update the session's state to LatestNonce 9, release the request, and wait for the drain to close.
//  5. Assert the hook finally read nonce 9, from after the last request ended.
func TestADrainingEscrowIsReadOnlyWhenItsLastRequestHasEnded(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.escrowState = types.EscrowState{LatestNonce: 7}
	var readMu sync.Mutex
	var readNonces []uint64
	read := func() []uint64 {
		readMu.Lock()
		defer readMu.Unlock()
		return append([]uint64(nil), readNonces...)
	}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Retiring: func(_ string, retiring EscrowSession) {
			readMu.Lock()
			defer readMu.Unlock()
			readNonces = append(readNonces, retiring.SnapshotState().LatestNonce)
		},
		Now: fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, release, acquired := registry.Acquire("1")
	require.True(t, acquired)

	require.NoError(t, registry.Retire("1"))
	require.Empty(t, read(), "the escrow was read while a request was still spending nonces on it")

	session.escrowState = types.EscrowState{LatestNonce: 9}
	release()
	awaitDrainClose(t, registry, func() bool { return len(read()) == 1 })

	require.Equal(t, []uint64{9}, read(), "the reading did not wait for the last request to end")
}

// Test flow:
//  1. Build a registry with one escrow session that was never added, and a Retiring hook counting reads.
//  2. Retire that escrow.
//  3. Assert the hook was never called: an escrow that was never published is not read as if it were retiring.
func TestRetiringAnEscrowThatIsNotRoutableReadsNothing(t *testing.T) {
	t.Parallel()
	reads := 0
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Retiring:        func(string, EscrowSession) { reads++ },
		Now:             fixedClock(),
	})

	require.NoError(t, registry.Retire("1"))

	require.Zero(t, reads, "an escrow that was never published was read as if it were retiring")
}
