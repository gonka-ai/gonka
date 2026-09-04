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
			ValidationRate:            5000,
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
		WarmKeys:                      map[uint32]string{0: "warm-0"},
		SealedAcc:                     []byte("sealed-acc-bytes"),
		HeightSyncForcedStart:         4,
		HeightSyncForcedEnd:           6,
		HeightSyncCadenceSwallowUntil: 12,
		HeightSyncSwallowFe:           6,
		HeightSyncTurnK:               10,
		HeightSyncTurnSlots:           3,
		HeightSyncTurnReason:          "heartbeat",
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
	require.Equal(t, state.HeightSyncForcedStart, roundTrip.HeightSyncForcedStart)
	require.Equal(t, state.HeightSyncForcedEnd, roundTrip.HeightSyncForcedEnd)
	require.Equal(t, state.HeightSyncCadenceSwallowUntil, roundTrip.HeightSyncCadenceSwallowUntil)
	require.Equal(t, state.HeightSyncSwallowFe, roundTrip.HeightSyncSwallowFe)
	require.Equal(t, state.HeightSyncTurnK, roundTrip.HeightSyncTurnK)
	require.Equal(t, state.HeightSyncTurnSlots, roundTrip.HeightSyncTurnSlots)
	require.Equal(t, state.HeightSyncTurnReason, roundTrip.HeightSyncTurnReason)
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

	data, err := MarshalStateSnapshotProto(state, committed, sealed, nil)
	require.NoError(t, err)

	roundTripState, roundTripCommitted, roundTripSealed, roundTripFloor, err := UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)
	require.Equal(t, state.EscrowID, roundTripState.EscrowID)
	require.Equal(t, state.LatestNonce, roundTripState.LatestNonce)
	require.Equal(t, committed, roundTripCommitted)
	require.Equal(t, sealed, roundTripSealed)
	require.Nil(t, roundTripFloor)
}

func TestMarshalStateSnapshotProtoRoundTripFloor(t *testing.T) {
	state := &EscrowState{
		EscrowID:                    "escrow-1",
		StateRootAndProtocolVersion: "v2",
		LatestNonce:                 42,
		Inferences:                  map[uint64]*InferenceRecord{},
		HostStats:                   map[uint32]*HostStats{0: {}},
		WarmKeys:                    map[uint32]string{0: "warm-0"},
	}
	floor := &FloorIndexProto{
		Truncated: true,
		Entries: []*FloorIndexEntryProto{{
			Nonce:  3,
			Height: 50,
			Hash:   []byte{0xaa},
			Author: 1,
		}},
	}

	data, err := MarshalStateSnapshotProto(state, nil, nil, floor)
	require.NoError(t, err)

	_, _, _, roundTripFloor, err := UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)
	require.NotNil(t, roundTripFloor)
	require.True(t, roundTripFloor.GetTruncated())
	require.Equal(t, floor.GetEntries()[0].GetHeight(), roundTripFloor.GetEntries()[0].GetHeight())
	require.Equal(t, floor.GetEntries()[0].GetHash(), roundTripFloor.GetEntries()[0].GetHash())
	require.Equal(t, floor.GetEntries()[0].GetAuthor(), roundTripFloor.GetEntries()[0].GetAuthor())
}
