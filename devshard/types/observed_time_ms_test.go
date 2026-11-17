package types

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestObservedTimeMs_DefaultsZero(t *testing.T) {
	require.Zero(t, (&MsgStartInference{}).GetObservedTimeMs())
	require.Zero(t, (&MsgConfirmStart{}).GetObservedTimeMs())
	require.Zero(t, (&MsgFinishInference{}).GetObservedTimeMs())
	require.Zero(t, (&MsgHeartbeat{}).GetObservedTimeMs())
	require.Zero(t, (&MsgHeightAck{}).GetObservedTimeMs())
	require.Zero(t, (&ExecutorReceiptContent{}).GetObservedTimeMs())
}

func TestObservedTimeMs_RoundTrip(t *testing.T) {
	cases := []proto.Message{
		&MsgStartInference{ObservedHeight: 10, ObservedBlockHash: []byte{1}, ObservedTimeMs: 111},
		&MsgConfirmStart{ObservedHeight: 10, ObservedBlockHash: []byte{1}, ObservedTimeMs: 222},
		&MsgFinishInference{ObservedHeight: 10, ObservedBlockHash: []byte{1}, ObservedTimeMs: 333},
		&MsgHeartbeat{ObservedHeight: 10, ObservedBlockHash: []byte{1}, ObservedTimeMs: 444},
		&MsgHeightAck{ObservedHeight: 10, ObservedBlockHash: []byte{1}, ObservedTimeMs: 555},
		&ExecutorReceiptContent{ObservedHeight: 10, ObservedBlockHash: []byte{1}, ObservedTimeMs: 666},
	}
	for _, msg := range cases {
		raw, err := proto.Marshal(msg)
		require.NoError(t, err)
		clone := proto.Clone(msg)
		proto.Reset(clone)
		require.NoError(t, proto.Unmarshal(raw, clone))
		switch got := clone.(type) {
		case *MsgStartInference:
			require.Equal(t, int64(111), got.GetObservedTimeMs())
		case *MsgConfirmStart:
			require.Equal(t, int64(222), got.GetObservedTimeMs())
		case *MsgFinishInference:
			require.Equal(t, int64(333), got.GetObservedTimeMs())
		case *MsgHeartbeat:
			require.Equal(t, int64(444), got.GetObservedTimeMs())
		case *MsgHeightAck:
			require.Equal(t, int64(555), got.GetObservedTimeMs())
		case *ExecutorReceiptContent:
			require.Equal(t, int64(666), got.GetObservedTimeMs())
		default:
			t.Fatalf("unexpected type %T", clone)
		}
	}
}
