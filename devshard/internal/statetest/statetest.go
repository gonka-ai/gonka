// Package statetest provides test helpers for state.StateMachine without
// creating an import cycle through devshard/internal/testutil.
package statetest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// MustStateMachine creates a StateMachine backed by an in-memory store for tests.
func MustStateMachine(
	t *testing.T,
	escrowID string,
	config types.SessionConfig,
	group []types.SlotAssignment,
	balance uint64,
	userAddr string,
	verifier signing.Verifier,
	opts ...state.SMOption,
) *state.StateMachine {
	t.Helper()
	sm, err := state.NewStateMachine(
		escrowID, config, group, balance, userAddr, verifier,
		testutil.MustMemoryStore(t, escrowID, userAddr, config, group, balance),
		opts...,
	)
	require.NoError(t, err)
	return sm
}

func ArmRefusalDue(t *testing.T, sm *state.StateMachine, inferenceID uint64) {
	t.Helper()
	const startHeight = 1
	st := sm.ExportState()
	rec, ok := st.Inferences[inferenceID]
	require.True(t, ok)
	rec.StartedAtHeight = startHeight
	rec.StartedAtBlockHash = []byte{0x01}
	rec.StartedAtTimeMs = startHeight * testutil.HeightClockBlockMs
	floor, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, sm.ExportHeightSyncFloor())
	require.NoError(t, err)
	if floor == nil {
		floor = heightsync.NewFloorIndex()
	}
	due := uint64(startHeight + st.Config.RefusalTimeout)
	floor.Observe(st.LatestNonce, []heightsync.FloorClaim{{Signer: 0, Height: due, Hash: []byte{0x02}}})
	require.NoError(t, sm.RestoreStateWithFloor(st, floor))
	require.NoError(t, sm.TimeoutDue(inferenceID, types.TimeoutReason_TIMEOUT_REASON_REFUSED))
}
