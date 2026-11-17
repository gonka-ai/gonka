package types

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestMsgStartConfirm_WallClockRoundTrip(t *testing.T) {
	start := &MsgStartInference{
		InferenceId:       7,
		PromptHash:        []byte{1, 2, 3},
		Model:             "m",
		InputLength:       10,
		MaxTokens:         20,
		ObservedHeight:    100,
		ObservedBlockHash: []byte{9},
		ObservedTimeMs:    111,
		StartedAt:         1_700_000_000,
	}
	raw, err := proto.Marshal(start)
	require.NoError(t, err)
	var startOut MsgStartInference
	require.NoError(t, proto.Unmarshal(raw, &startOut))
	require.Equal(t, int64(1_700_000_000), startOut.GetStartedAt())
	require.Equal(t, start.GetObservedTimeMs(), startOut.GetObservedTimeMs())

	confirm := &MsgConfirmStart{
		InferenceId:       7,
		ExecutorSig:       []byte{4, 5},
		ObservedHeight:    101,
		ObservedBlockHash: []byte{8},
		ObservedTimeMs:    222,
		ConfirmedAt:       1_700_000_042,
	}
	raw, err = proto.Marshal(confirm)
	require.NoError(t, err)
	var confirmOut MsgConfirmStart
	require.NoError(t, proto.Unmarshal(raw, &confirmOut))
	require.Equal(t, int64(1_700_000_042), confirmOut.GetConfirmedAt())
	require.Equal(t, confirm.GetObservedTimeMs(), confirmOut.GetObservedTimeMs())
}

func TestWallClock_OriginalFieldNumbersRestored(t *testing.T) {
	// Same type (int64) and meaning (Unix seconds) as before the stamp-only
	// tombstone — restore the original numbers so historical wire still
	// decodes, instead of inventing parallel tags.
	cases := []struct {
		name string
		md   protoreflect.MessageDescriptor
		live map[protoreflect.FieldNumber]string
	}{
		{
			name: "MsgStartInference",
			md:   (&MsgStartInference{}).ProtoReflect().Descriptor(),
			live: map[protoreflect.FieldNumber]string{6: "started_at"},
		},
		{
			name: "MsgConfirmStart",
			md:   (&MsgConfirmStart{}).ProtoReflect().Descriptor(),
			live: map[protoreflect.FieldNumber]string{3: "confirmed_at"},
		},
		{
			name: "ExecutorReceiptContent",
			md:   (&ExecutorReceiptContent{}).ProtoReflect().Descriptor(),
			live: map[protoreflect.FieldNumber]string{
				6: "started_at",
				8: "confirmed_at",
			},
		},
		{
			name: "InferenceRecordProto",
			md:   (&InferenceRecordProto{}).ProtoReflect().Descriptor(),
			live: map[protoreflect.FieldNumber]string{
				13: "started_at",
				14: "confirmed_at",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for n, want := range tc.live {
				require.False(t, tc.md.ReservedRanges().Has(n), "%s field %d must not stay reserved", tc.name, n)
				fd := tc.md.Fields().ByNumber(n)
				require.NotNil(t, fd, "%s missing live field %d", tc.name, n)
				require.Equal(t, protoreflect.Name(want), fd.Name())
				require.Equal(t, protoreflect.Int64Kind, fd.Kind())
			}
		})
	}
}

func TestExecutorReceiptContent_WallClocksInDeterministicMarshal(t *testing.T) {
	content := &ExecutorReceiptContent{
		InferenceId:       3,
		PromptHash:        []byte("ph"),
		Model:             "m",
		InputLength:       4,
		MaxTokens:         5,
		EscrowId:          "escrow-1",
		ObservedHeight:    9,
		ObservedBlockHash: []byte{1},
		ObservedTimeMs:    99,
		StartedAt:         1_700_000_100,
		ConfirmedAt:       1_700_000_110,
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(content)
	require.NoError(t, err)

	var decoded ExecutorReceiptContent
	require.NoError(t, proto.Unmarshal(raw, &decoded))
	require.Equal(t, content.GetStartedAt(), decoded.GetStartedAt())
	require.Equal(t, content.GetConfirmedAt(), decoded.GetConfirmedAt())

	// Omitting wall clocks must change the signed preimage bytes.
	withoutWall := proto.Clone(content).(*ExecutorReceiptContent)
	withoutWall.StartedAt = 0
	withoutWall.ConfirmedAt = 0
	rawNoWall, err := proto.MarshalOptions{Deterministic: true}.Marshal(withoutWall)
	require.NoError(t, err)
	require.NotEqual(t, raw, rawNoWall, "wall started_at/confirmed_at must be part of executor_sig preimage")
}

func TestInferenceRecordProto_WallClockRoundTrip(t *testing.T) {
	rec := &InferenceRecord{
		Status:      StatusStarted,
		Model:       "m",
		PromptHash:  []byte{1},
		StartedAt:   1_700_000_200,
		ConfirmedAt: 1_700_000_210,
	}
	msg := inferenceRecordToProto(1, rec)
	require.Equal(t, int64(1_700_000_200), msg.GetStartedAt())
	require.Equal(t, int64(1_700_000_210), msg.GetConfirmedAt())

	raw, err := proto.Marshal(msg)
	require.NoError(t, err)
	var out InferenceRecordProto
	require.NoError(t, proto.Unmarshal(raw, &out))
	got := inferenceRecordFromProto(&out)
	require.Equal(t, rec.StartedAt, got.StartedAt)
	require.Equal(t, rec.ConfirmedAt, got.ConfirmedAt)
}
