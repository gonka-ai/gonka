package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEscrowStateProtoRoundTrip(t *testing.T) {
	state := &EscrowState{
		EscrowID:                    "escrow-1",
		StateRootAndProtocolVersion: DevshardStateRootAndProtocolVersion,
		Config: SessionConfig{
			RefusalTimeout:            60,
			ExecutionTimeout:          1200,
			TokenPrice:                1,
			CreateDevshardFee:         10_000,
			FeePerNonce:               1_000,
			VoteThreshold:             1,
			ValidationRate:            DefaultValidationRate,
			InferenceSealGraceNonces:  20,
			InferenceSealGraceSeconds: 120,
		},
		Group: []SlotAssignment{
			{SlotID: 0, ValidatorAddress: "host-0"},
		},
		Balance:       1000,
		Fees:          42,
		Phase:         PhaseFinalizing,
		FinalizeNonce: 7,
		LatestNonce:   9,
		Inferences: map[uint64]*InferenceRecord{
			1: {
				Status:       StatusStarted,
				ExecutorSlot: 0,
				Model:        "model-a",
				PromptHash:   []byte("prompt"),
				InputLength:  10,
				MaxTokens:    100,
				ReservedCost: 50,
				StartedAt:    123,
				ValidatedBy:  Bitmap128{1, 0},
			},
		},
		HostStats: map[uint32]*HostStats{
			0: {Missed: 1, Invalid: 2, Cost: 3},
		},
		WarmKeys:  map[uint32]string{0: "warm-0"},
		SealedAcc: []byte("sealed-acc-bytes"),
	}

	roundTrip := EscrowStateFromProto(EscrowStateToProto(state))
	require.Equal(t, state.EscrowID, roundTrip.EscrowID)
	require.Equal(t, state.StateRootAndProtocolVersion, roundTrip.StateRootAndProtocolVersion)
	require.Equal(t, state.Config, roundTrip.Config)
	require.Equal(t, state.Group, roundTrip.Group)
	require.Equal(t, state.Balance, roundTrip.Balance)
	require.Equal(t, state.Fees, roundTrip.Fees)
	require.Equal(t, state.Phase, roundTrip.Phase)
	require.Equal(t, state.FinalizeNonce, roundTrip.FinalizeNonce)
	require.Equal(t, state.LatestNonce, roundTrip.LatestNonce)
	require.Equal(t, state.SealedAcc, roundTrip.SealedAcc)
	require.Equal(t, state.WarmKeys, roundTrip.WarmKeys)
	require.Equal(t, state.HostStats[0], roundTrip.HostStats[0])
	require.Equal(t, state.Inferences[1], roundTrip.Inferences[1])
}

func TestMarshalStateSnapshotProtoRoundTrip(t *testing.T) {
	state := &EscrowState{
		EscrowID:                    "escrow-1",
		StateRootAndProtocolVersion: "v2",
		LatestNonce:                 42,
		Inferences:                  map[uint64]*InferenceRecord{},
		HostStats:                   map[uint32]*HostStats{0: {}},
		WarmKeys:                    map[uint32]string{0: "warm-0"},
	}
	committed := map[uint64][]byte{1: []byte("entry-one")}
	sealed := map[uint64]uint64{1: 11}

	data, err := MarshalStateSnapshotProto(state, committed, sealed)
	require.NoError(t, err)

	roundTripState, roundTripCommitted, roundTripSealed, err := UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)
	require.Equal(t, state.EscrowID, roundTripState.EscrowID)
	require.Equal(t, state.LatestNonce, roundTripState.LatestNonce)
	require.Equal(t, committed, roundTripCommitted)
	require.Equal(t, sealed, roundTripSealed)
}
