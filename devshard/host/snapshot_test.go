package host

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestUnmarshalStateSnapshot_RejectsJSON(t *testing.T) {
	data := []byte(`{"state":{"escrowID":"escrow-1"}}`)
	_, err := UnmarshalStateSnapshot(data)
	require.Error(t, err)
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
	require.NotEmpty(t, data)

	roundTripState, roundTripCommitted, roundTripSealed, err := UnmarshalStateSnapshotWithCommitted(data)
	require.NoError(t, err)
	require.Equal(t, state.EscrowID, roundTripState.EscrowID)
	require.Equal(t, state.LatestNonce, roundTripState.LatestNonce)
	require.Equal(t, committed, roundTripCommitted)
	require.Equal(t, sealed, roundTripSealed)
}
