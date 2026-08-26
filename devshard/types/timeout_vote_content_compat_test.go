package types

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Pre-change TimeoutVoteContent encodings, captured before bytes response_hash = 5
// was added. proto3 omits empty bytes, so REFUSED / EXECUTION contents must stay
// byte-identical. A mismatch here means either the field was populated for an
// existing reason, or it was encoded as a zero-length bytes rather than omitted.
var timeoutVoteContentPreChange = map[string]string{
	"refused_accept":   "0a08657363726f772d31100118012001",
	"execution_accept": "0a08657363726f772d31100118022001",
	"refused_reject":   "0a08657363726f772d3110011801",
}

func TestTimeoutReasonErrorValue(t *testing.T) {
	if TimeoutReason_TIMEOUT_REASON_UNSPECIFIED != 0 {
		t.Fatalf("UNSPECIFIED = %d, want 0", TimeoutReason_TIMEOUT_REASON_UNSPECIFIED)
	}
	if TimeoutReason_TIMEOUT_REASON_REFUSED != 1 {
		t.Fatalf("REFUSED = %d, want 1", TimeoutReason_TIMEOUT_REASON_REFUSED)
	}
	if TimeoutReason_TIMEOUT_REASON_EXECUTION != 2 {
		t.Fatalf("EXECUTION = %d, want 2", TimeoutReason_TIMEOUT_REASON_EXECUTION)
	}
	if TimeoutReason_TIMEOUT_REASON_ERROR != 3 {
		t.Fatalf("ERROR = %d, want 3", TimeoutReason_TIMEOUT_REASON_ERROR)
	}
}

func TestTimeoutVoteContentResponseHashFieldNumber(t *testing.T) {
	md := (&TimeoutVoteContent{}).ProtoReflect().Descriptor()
	fd := md.Fields().ByName("response_hash")
	if fd == nil {
		t.Fatal("TimeoutVoteContent missing response_hash")
	}
	if fd.Number() != 5 {
		t.Fatalf("response_hash field number = %d, want 5", fd.Number())
	}
	if fd.Kind() != protoreflect.BytesKind {
		t.Fatalf("response_hash kind = %s, want bytes", fd.Kind())
	}
}

func TestMsgTimeoutInferenceUnchanged(t *testing.T) {
	md := (&MsgTimeoutInference{}).ProtoReflect().Descriptor()
	if md.Fields().Len() != 3 {
		t.Fatalf("MsgTimeoutInference field count = %d, want 3 (inference_id, reason, votes)", md.Fields().Len())
	}
	if md.Fields().ByNumber(1).Name() != "inference_id" {
		t.Fatalf("field 1 = %s, want inference_id", md.Fields().ByNumber(1).Name())
	}
	if md.Fields().ByNumber(2).Name() != "reason" {
		t.Fatalf("field 2 = %s, want reason", md.Fields().ByNumber(2).Name())
	}
	if md.Fields().ByNumber(3).Name() != "votes" {
		t.Fatalf("field 3 = %s, want votes", md.Fields().ByNumber(3).Name())
	}
}

func TestTimeoutVoteUnchanged(t *testing.T) {
	md := (&TimeoutVote{}).ProtoReflect().Descriptor()
	if md.Fields().Len() != 3 {
		t.Fatalf("TimeoutVote field count = %d, want 3 (voter_slot, accept, signature)", md.Fields().Len())
	}
}

func TestTimeoutVoteContentExistingReasonsMarshalIdentically(t *testing.T) {
	cases := []struct {
		name    string
		content *TimeoutVoteContent
	}{
		{
			name: "refused_accept",
			content: &TimeoutVoteContent{
				EscrowId:    "escrow-1",
				InferenceId: 1,
				Reason:      TimeoutReason_TIMEOUT_REASON_REFUSED,
				Accept:      true,
			},
		},
		{
			name: "execution_accept",
			content: &TimeoutVoteContent{
				EscrowId:    "escrow-1",
				InferenceId: 1,
				Reason:      TimeoutReason_TIMEOUT_REASON_EXECUTION,
				Accept:      true,
			},
		},
		{
			name: "refused_reject",
			content: &TimeoutVoteContent{
				EscrowId:    "escrow-1",
				InferenceId: 1,
				Reason:      TimeoutReason_TIMEOUT_REASON_REFUSED,
				Accept:      false,
			},
		},
	}
	det := proto.MarshalOptions{Deterministic: true}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := hex.DecodeString(timeoutVoteContentPreChange[tc.name])
			if err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			got, err := proto.Marshal(tc.content)
			if err != nil {
				t.Fatalf("proto.Marshal: %v", err)
			}
			if hex.EncodeToString(got) != hex.EncodeToString(want) {
				t.Fatalf("proto.Marshal = %s, want %s (empty response_hash must be omitted)", hex.EncodeToString(got), hex.EncodeToString(want))
			}
			detGot, err := det.Marshal(tc.content)
			if err != nil {
				t.Fatalf("deterministic Marshal: %v", err)
			}
			if hex.EncodeToString(detGot) != hex.EncodeToString(want) {
				t.Fatalf("deterministic Marshal = %s, want %s", hex.EncodeToString(detGot), hex.EncodeToString(want))
			}
		})
	}
}

func TestTimeoutVoteContentEmptyHashOmittedNotZeroLength(t *testing.T) {
	empty := &TimeoutVoteContent{
		EscrowId:     "escrow-1",
		InferenceId:  1,
		Reason:       TimeoutReason_TIMEOUT_REASON_ERROR,
		Accept:       true,
		ResponseHash: nil,
	}
	zero := &TimeoutVoteContent{
		EscrowId:     "escrow-1",
		InferenceId:  1,
		Reason:       TimeoutReason_TIMEOUT_REASON_ERROR,
		Accept:       true,
		ResponseHash: []byte{},
	}
	emptyBytes, err := proto.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal nil hash: %v", err)
	}
	zeroBytes, err := proto.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal empty hash: %v", err)
	}
	if hex.EncodeToString(emptyBytes) != hex.EncodeToString(zeroBytes) {
		t.Fatalf("nil hash %s != empty hash %s; both must omit field 5", hex.EncodeToString(emptyBytes), hex.EncodeToString(zeroBytes))
	}
	populated := &TimeoutVoteContent{
		EscrowId:     "escrow-1",
		InferenceId:  1,
		Reason:       TimeoutReason_TIMEOUT_REASON_ERROR,
		Accept:       true,
		ResponseHash: []byte{0xab, 0xcd},
	}
	popBytes, err := proto.Marshal(populated)
	if err != nil {
		t.Fatalf("marshal populated hash: %v", err)
	}
	if hex.EncodeToString(popBytes) == hex.EncodeToString(emptyBytes) {
		t.Fatal("populated response_hash marshaled identically to empty; field 5 is not being encoded")
	}
}
