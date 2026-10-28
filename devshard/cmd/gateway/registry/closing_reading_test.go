package registry

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Test flow:
//  1. Build a registry with two escrow sessions reporting LatestNonce 7 and 9, and a Closing hook that records each escrow, the nonce it reads and its session's close-call count when it fires.
//  2. Add both escrows, keep a request on the second and retire it, so one is live and one is draining.
//  3. Close the registry.
//  4. Assert the hook read both escrows, each before its session closed, and every session was closed once afterwards.
func TestClosingTheRegistryReadsEveryEscrowBeforeItsSessionCloses(t *testing.T) {
	t.Parallel()
	live, draining := newFakeSession("hostA"), newFakeSession("hostB")
	live.escrowState = types.EscrowState{LatestNonce: 7}
	draining.escrowState = types.EscrowState{LatestNonce: 9}
	sessions := map[string]*fakeSession{"1": live, "2": draining}
	readNonces := map[string]uint64{}
	closedWhenRead := map[string]int64{}
	registry := New(Deps{
		ServingSessions: newSessions(sessions).open,
		Closing: func(escrowID string, closing EscrowSession) {
			readNonces[escrowID] = closing.SnapshotState().LatestNonce
			closedWhenRead[escrowID] = sessions[escrowID].closeCalls.Load()
		},
		Now: fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")
	_, release, acquired := registry.Acquire("2")
	require.True(t, acquired)
	require.NoError(t, registry.Retire("2"))

	require.NoError(t, registry.Close())
	release()

	require.Equal(t, map[string]uint64{"1": 7, "2": 9}, readNonces, "the shutdown did not read every escrow it held")
	require.Equal(t, map[string]int64{"1": 0, "2": 0}, closedWhenRead, "a session was already closed when the shutdown read it")
	require.Equal(t, int64(1), live.closeCalls.Load())
	require.Equal(t, int64(1), draining.closeCalls.Load())
}

// Test flow:
//  1. Build a registry with one escrow session, a Retiring hook and a Closing hook that each count their calls.
//  2. Add the escrow and close the registry.
//  3. Assert the Closing hook fired once and the Retiring hook never did: a shutdown reads the escrow without retiring it.
func TestClosingTheRegistryDoesNotRetireItsEscrows(t *testing.T) {
	t.Parallel()
	retired, closed := 0, 0
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Retiring:        func(string, EscrowSession) { retired++ },
		Closing:         func(string, EscrowSession) { closed++ },
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	require.NoError(t, registry.Close())

	require.Equal(t, 1, closed, "the shutdown did not read the escrow it held")
	require.Zero(t, retired, "the shutdown retired the escrow it only closed")
}
