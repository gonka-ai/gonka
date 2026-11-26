package transport

import (
	"errors"
	"time"

	"devshard/host"
	"devshard/transport/rpcpb"
	"devshard/types"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

var ErrVerifyRequestTooLarge = errors.New("encoded verification request exceeds the host body limit")

func VerifyTimeoutFits(escrowID string, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff) (bool, error) {
	wire, err := verifyDiffs(diffs)
	if err != nil {
		return false, err
	}
	req := VerifyTimeoutRequest{
		InferenceID: inferenceID, Reason: TimeoutReasonToString(reason),
		Payload: timeoutVotePayload(reason, payload), Diffs: wire,
	}
	return verifyEnvelopeFits(escrowID, VerifyTimeoutRequestToProto(req)), nil
}

func VerifyErrorMissFits(escrowID string, inferenceID uint64, artifacts host.TimeoutArtifacts, diffs []types.Diff) (bool, error) {
	wire, err := verifyDiffs(diffs)
	if err != nil {
		return false, err
	}
	req := VerifyErrorMissRequest{
		InferenceID: inferenceID, Diffs: wire,
		FinishTx: artifacts.FinishTx, ResponsePayload: artifacts.ResponsePayload,
	}
	return verifyEnvelopeFits(escrowID, VerifyErrorMissRequestToProto(req)), nil
}

func verifyEnvelopeFits(escrowID string, req proto.Message) bool {
	const recoverableSignatureSize = 65
	size := proto.Size(&rpcpb.SignedEnvelope{
		Signature: make([]byte, recoverableSignatureSize), Timestamp: time.Now().Unix(), EscrowId: escrowID,
	})
	if payload := proto.Size(req); payload > 0 {
		size += protowire.SizeTag(1) + protowire.SizeBytes(payload)
	}
	return int64(size) <= DefaultRPCLargeReadMaxBytes
}

func verifyDiffs(diffs []types.Diff) ([]DiffJSON, error) {
	wire := make([]DiffJSON, len(diffs))
	for i, diff := range diffs {
		row, err := DiffToJSON(diff)
		if err != nil {
			return nil, err
		}
		wire[i] = row
	}
	return wire, nil
}
