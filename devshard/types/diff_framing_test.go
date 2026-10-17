package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCheckDiffContentFraming(t *testing.T) {
	valid, err := proto.Marshal(&DiffContent{Txs: []*DevshardTx{
		{Tx: &DevshardTx_StartInference{StartInference: &MsgStartInference{
			InferenceId: 1,
			Model:       "m",
			PromptHash:  bytes.Repeat([]byte{0xab}, 32),
		}}},
		{Tx: &DevshardTx_TimeoutInference{TimeoutInference: &MsgTimeoutInference{
			InferenceId: 1,
			Votes:       []*TimeoutVote{{VoterSlot: 2, Accept: true, Signature: []byte{1, 2, 3}}},
		}}},
	}})
	require.NoError(t, err)

	// Field 2 (txs) length covers a start_inference whose own length overruns it.
	inner := []byte{0x12, 0x03, 0x0a, 0x05, 0x00}
	// Field 1 (nonce) sent as a length-delimited blob.
	wrongType := []byte{0x0a, 0x01, 0x01}
	// Field 3 (escrow_id) is not UTF-8.
	badUTF8 := []byte{0x1a, 0x02, 0xff, 0xff}

	cases := []struct {
		name string
		in   []byte
	}{
		{name: "empty", in: nil},
		{name: "stored txs", in: valid},
		{name: "truncated", in: valid[:len(valid)-1]},
		{name: "inner overrun", in: inner},
		{name: "wrong wire type", in: wrongType},
		{name: "bad utf-8", in: badUTF8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, decodeErr := DecodeDiffWire(DiffWire{Txs: tc.in})
			frameErr := CheckDiffContentFraming(tc.in)
			if decodeErr == nil {
				require.NoError(t, frameErr)
				return
			}
			require.Error(t, frameErr)
		})
	}
}

func TestCheckDiffContentFramingAllocsIgnoreTxCount(t *testing.T) {
	oneTx := []*DevshardTx{{
		Tx: &DevshardTx_StartInference{StartInference: &MsgStartInference{
			PromptHash: bytes.Repeat([]byte{0xab}, 64),
		}},
	}}
	const n = 200
	manyTx := make([]*DevshardTx, n)
	for i := range manyTx {
		manyTx[i] = &DevshardTx{Tx: &DevshardTx_StartInference{StartInference: &MsgStartInference{
			PromptHash: []byte{byte(i)},
		}}}
	}
	one, err := proto.Marshal(&DiffContent{Txs: oneTx})
	require.NoError(t, err)
	many, err := proto.Marshal(&DiffContent{Txs: manyTx})
	require.NoError(t, err)
	require.NoError(t, CheckDiffContentFraming(one))
	require.NoError(t, CheckDiffContentFraming(many))

	oneAllocs := testing.AllocsPerRun(50, func() {
		if err := CheckDiffContentFraming(one); err != nil {
			panic(err)
		}
	})
	manyAllocs := testing.AllocsPerRun(50, func() {
		if err := CheckDiffContentFraming(many); err != nil {
			panic(err)
		}
	})
	require.Zero(t, oneAllocs)
	require.Zero(t, manyAllocs)
}
