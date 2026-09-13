package transport

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/heightsync"
	"devshard/transport/rpcpb"
	"devshard/types"
)

func TestDiffJSON_ProtoRoundTrip(t *testing.T) {
	in := DiffJSON{
		Nonce:         7,
		Txs:           []byte{0x0a, 0x01, 0x02},
		UserSig:       []byte{0xab, 0xcd},
		PostStateRoot: []byte{0x11, 0x22, 0x33},
	}
	got := DiffJSONFromProto(DiffJSONToProto(in))
	require.Equal(t, in, got)
}

func TestPayloadJSON_ProtoRoundTrip(t *testing.T) {
	in := &PayloadJSON{
		Prompt:      []byte("hello"),
		Model:       "m",
		InputLength: 5,
		MaxTokens:   16,
		StartedAt:   123,
	}
	got := PayloadJSONFromProto(PayloadJSONToProto(in))
	require.Equal(t, in, got)
	require.Nil(t, PayloadJSONFromProto(PayloadJSONToProto(nil)))
}

func TestVerifyTimeout_ProtoRoundTrip(t *testing.T) {
	in := VerifyTimeoutRequest{
		InferenceID: 9,
		Reason:      "refused",
		Payload:     &PayloadJSON{Model: "x", Prompt: []byte{1}},
		Diffs:       []DiffJSON{{Nonce: 1, Txs: []byte{2}, UserSig: []byte{3}}},
	}
	got := VerifyTimeoutRequestFromProto(VerifyTimeoutRequestToProto(in))
	require.Equal(t, in, got)

	resp := &VerifyTimeoutResponse{
		Accept:      true,
		Signature:   []byte{9},
		VoterSlot:   2,
		Mempool:     [][]byte{{0x01}, {0x02}},
		RejectCause: "",
	}
	require.Equal(t, resp, VerifyTimeoutResponseFromProto(VerifyTimeoutResponseToProto(resp)))
}

func TestVerifyErrorMiss_ProtoRoundTrip(t *testing.T) {
	in := VerifyErrorMissRequest{
		InferenceID:     4,
		Diffs:           []DiffJSON{{Nonce: 2, Txs: []byte{1}, UserSig: []byte{2}}},
		FinishTx:        []byte{3, 4},
		ResponsePayload: []byte("err"),
	}
	require.Equal(t, in, VerifyErrorMissRequestFromProto(VerifyErrorMissRequestToProto(in)))
	resp := &VerifyErrorMissResponse{Accept: false, RejectCause: "no_finish_tx", Mempool: [][]byte{{5}}}
	require.Equal(t, resp, VerifyErrorMissResponseFromProto(VerifyErrorMissResponseToProto(resp)))
}

func TestChallengeReceipt_ProtoRoundTrip(t *testing.T) {
	in := ChallengeReceiptRequest{
		InferenceID: 3,
		Payload:     &PayloadJSON{Model: "m", Prompt: []byte("p")},
		Diffs:       []DiffJSON{{Nonce: 1, Txs: []byte{1}, UserSig: []byte{1}}},
	}
	require.Equal(t, in, ChallengeReceiptRequestFromProto(ChallengeReceiptRequestToProto(in)))
	resp := &ChallengeReceiptResponse{Receipt: []byte{1, 2}, Mempool: [][]byte{{3}}}
	require.Equal(t, resp, ChallengeReceiptResponseFromProto(ChallengeReceiptResponseToProto(resp)))
}

func TestRepair_ProtoRoundTrip(t *testing.T) {
	in := &heightsync.RepairRequest{
		TurnStart:         10,
		RefNonce:          3,
		RequesterSlot:     1,
		ObservedHeight:    500,
		ObservedBlockHash: []byte{0xaa},
		RequesterSig:      []byte{0xbb},
	}
	require.Equal(t, in, RepairRequestFromProto(RepairRequestToProto(in)))
	resp := &heightsync.RepairResponse{
		Outcome:           heightsync.RepairOutcomeHeight,
		ObservedHeight:    510,
		ObservedBlockHash: []byte{0xcc},
		SyncState:         types.SyncState(1),
		ResponderSig:      []byte{0xdd},
	}
	require.Equal(t, resp, RepairResponseFromProto(RepairResponseToProto(resp)))
}

func TestHeightSyncSection_ProtoRoundTrip(t *testing.T) {
	in := &heightsync.HeightSyncSection{
		ChainID:               "c",
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         42,
		MainnetBlockHashHex:   "abcd",
		TimestampUnixMs:       9,
		Direction:             "response",
		OriginatorSenderID:    "gonka1x",
		OriginatorTimestampMs: 8,
		SenderSignature:       []byte{1, 2, 3},
		TipStaleAfterMs:       100,
	}
	require.Equal(t, in, HeightSyncSectionFromProto(HeightSyncSectionToProto(in)))
}

func TestGossipNonce_ProtoRoundTrip(t *testing.T) {
	in := GossipNonceRequest{Nonce: 5, StateHash: []byte{1}, StateSig: []byte{2}, SlotID: 0}
	require.Equal(t, in, GossipNonceRequestFromProto(GossipNonceRequestToProto(in)))
}

func TestDiffJSON_ProtoBytesMatchJSONForm(t *testing.T) {
	in := DiffJSON{Nonce: 1, Txs: []byte("txs-bytes"), UserSig: []byte("sig")}
	pb := DiffJSONToProto(in)
	require.True(t, bytes.Equal(in.Txs, pb.GetTxs()))
	require.True(t, bytes.Equal(in.UserSig, pb.GetUserSig()))
}

func TestDiffsJSON_EmptyAndNil(t *testing.T) {
	require.Nil(t, diffsJSONToProto(nil))
	require.Nil(t, diffsJSONToProto([]DiffJSON{}))
	require.Nil(t, diffsJSONFromProto(nil))
	require.Nil(t, diffsJSONFromProto([]*rpcpb.Diff{}))

	// Empty collapses to nil on round-trip; JSON unmarshal of "[]" would keep a non-nil slice.
	empty := VerifyTimeoutRequest{InferenceID: 1, Diffs: []DiffJSON{}}
	got := VerifyTimeoutRequestFromProto(VerifyTimeoutRequestToProto(empty))
	require.Nil(t, got.Diffs)

	nilDiffs := VerifyTimeoutRequest{InferenceID: 1}
	gotNil := VerifyTimeoutRequestFromProto(VerifyTimeoutRequestToProto(nilDiffs))
	require.Nil(t, gotNil.Diffs)

	challenge := ChallengeReceiptRequest{InferenceID: 9, Diffs: []DiffJSON{}}
	require.Nil(t, ChallengeReceiptRequestFromProto(ChallengeReceiptRequestToProto(challenge)).Diffs)
}

func TestGossipTxs_BytesRoundTrip(t *testing.T) {
	txs := []*types.DevshardTx{
		{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{SlotsNum: 3}}},
	}
	raw, err := DevshardTxsToBytes(txs)
	require.NoError(t, err)
	require.Len(t, raw, 1)

	// Same path as RPCClient.GossipTxs / GossipHandler.Txs: no rpcconv ToProto pair.
	wire, err := proto.Marshal(&rpcpb.GossipTxsRequest{Txs: raw})
	require.NoError(t, err)
	var inner rpcpb.GossipTxsRequest
	require.NoError(t, proto.Unmarshal(wire, &inner))
	got, err := DevshardTxsFromBytes(inner.GetTxs())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, uint64(3), got[0].GetHeartbeat().GetSlotsNum())

	empty, err := DevshardTxsToBytes(nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	fromNil, err := DevshardTxsFromBytes(nil)
	require.NoError(t, err)
	require.Empty(t, fromNil)
	fromEmpty, err := DevshardTxsFromBytes([][]byte{})
	require.NoError(t, err)
	require.Empty(t, fromEmpty)

	emptyWire, err := proto.Marshal(&rpcpb.GossipTxsRequest{Txs: empty})
	require.NoError(t, err)
	var emptyInner rpcpb.GossipTxsRequest
	require.NoError(t, proto.Unmarshal(emptyWire, &emptyInner))
	decoded, err := DevshardTxsFromBytes(emptyInner.GetTxs())
	require.NoError(t, err)
	require.Empty(t, decoded)
}
