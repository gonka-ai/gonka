package host

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestUnmarshalStateSnapshot_LegacyRevealedSeedsIgnored(t *testing.T) {
	data := []byte(`{
		"state": {
			"escrowID": "escrow-1",
			"version": "v1",
			"phase": 1,
			"finalizeNonce": 7,
			"latestNonce": 9,
			"revealedSeeds": {
				"0": 123,
				"1": 456
			},
			"hostStats": {
				"0": {"missed": 1},
				"1": {"invalid": 2}
			},
			"warmKeys": {
				"0": "warm-0"
			}
		}
	}`)

	state, err := UnmarshalStateSnapshot(data)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, "escrow-1", state.EscrowID)
	require.Equal(t, uint64(7), state.FinalizeNonce)
	require.Equal(t, uint64(9), state.LatestNonce)
	require.Equal(t, "warm-0", state.WarmKeys[0])
	require.Equal(t, uint32(1), state.HostStats[0].Missed)
	require.Equal(t, uint32(2), state.HostStats[1].Invalid)
}

func TestMarshalStateSnapshotWithCommitted_RoundTrip(t *testing.T) {
	state := &types.EscrowState{
		EscrowID:                    "escrow-1",
		StateRootAndProtocolVersion: "v1",
		LatestNonce:                 42,
		Inferences:                  map[uint64]*types.InferenceRecord{},
		HostStats:                   map[uint32]*types.HostStats{0: {}},
		WarmKeys:                    map[uint32]string{0: "warm-0"},
	}
	committed := map[uint64][]byte{
		1: []byte("entry-one"),
		2: []byte("entry-two"),
	}
	sealed := map[uint64]uint64{
		1: 11,
		2: 22,
	}

	data, err := MarshalStateSnapshotWithCommitted(state, committed, sealed)
	require.NoError(t, err)

	roundTripState, roundTripCommitted, roundTripSealed, err := UnmarshalStateSnapshotWithCommitted(data)
	require.NoError(t, err)
	require.Equal(t, state.EscrowID, roundTripState.EscrowID)
	require.Equal(t, state.LatestNonce, roundTripState.LatestNonce)
	require.Equal(t, committed, roundTripCommitted)
	require.Equal(t, sealed, roundTripSealed)
}
