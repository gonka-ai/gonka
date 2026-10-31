package types

import (
	"devshard/signing"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
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
		WarmKeyHistory: signing.WarmKeyHistory{
			0: {{Addr: "warm-0", FromNonce: 3, GrantID: 7}},
			1: {{Addr: "warm-1", FromNonce: 2, GrantID: 1, RevokedAtNonce: 9}, {Addr: "warm-1b", FromNonce: 10, GrantID: 2}},
		},
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
	require.Equal(t, state.WarmKeyHistory, roundTrip.WarmKeyHistory)
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
		WarmKeyHistory:              signing.WarmKeyHistory{0: {{Addr: "warm-0", FromNonce: 1, GrantID: 1}}},
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
		WarmKeyHistory:              signing.WarmKeyHistory{0: {{Addr: "warm-0", FromNonce: 1, GrantID: 1}}},
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
	require.True(t, roundTripFloor.Truncated)
	require.Equal(t, floor.Entries[0].Height, roundTripFloor.Entries[0].Height)
	require.Equal(t, floor.Entries[0].Hash, roundTripFloor.Entries[0].Hash)
	require.Equal(t, floor.Entries[0].Author, roundTripFloor.Entries[0].Author)
}

// Test flow:
//  1. Marshal a snapshot whose state carries the reserved v6 warm_keys field (11).
//  2. Assert the decoder refuses it as a snapshot of an earlier protocol, and accepts the same snapshot without it.
func TestUnmarshalStateSnapshotProto_RefusesV6WarmKeys(t *testing.T) {
	state := &EscrowState{EscrowID: "escrow-1", StateRootAndProtocolVersion: "v6", Inferences: map[uint64]*InferenceRecord{}, HostStats: map[uint32]*HostStats{}}
	data, err := MarshalStateSnapshotProto(state, nil, nil, nil)
	require.NoError(t, err)
	_, _, _, _, err = UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)

	msg := &StateSnapshotProto{}
	require.NoError(t, proto.Unmarshal(data, msg))
	// map<uint32, string> warm_keys = 11, one entry {1: "warm"}.
	entry := protowire.AppendTag(nil, 1, protowire.VarintType)
	entry = protowire.AppendVarint(entry, 1)
	entry = protowire.AppendTag(entry, 2, protowire.BytesType)
	entry = protowire.AppendString(entry, "warm")
	legacy := protowire.AppendTag(nil, 11, protowire.BytesType)
	legacy = protowire.AppendBytes(legacy, entry)
	msg.State.ProtoReflect().SetUnknown(legacy)
	withLegacy, err := proto.Marshal(msg)
	require.NoError(t, err)
	_, _, _, _, err = UnmarshalStateSnapshotProto(withLegacy)
	require.ErrorIs(t, err, ErrSnapshotFromEarlierProtocol)
}
