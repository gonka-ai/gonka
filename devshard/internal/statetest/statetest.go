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
	// Production sessions use the v7 tag and reject a start while the floor
	// is empty. Fixtures that are not about that rule stay on v6, and a
	// version passed in opts still wins.
	opts = append([]state.SMOption{state.WithStateRootAndProtocolVersion("v6")}, opts...)
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
	// Carry TimeMs so TimeoutDue (StoredAt-only under lock) is armed without a map row.
	floor.Observe(st.LatestNonce, []heightsync.FloorClaim{{
		Signer: 0, Height: due, Hash: []byte{0x02},
		TimeMs: int64(due) * testutil.HeightClockBlockMs,
	}})
	require.NoError(t, sm.RestoreStateWithFloor(st, floor))
	require.NoError(t, sm.TimeoutDue(inferenceID, types.TimeoutReason_TIMEOUT_REASON_REFUSED))
}
