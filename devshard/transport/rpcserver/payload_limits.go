package rpcserver

import (
	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"devshard/transport"
	"devshard/transport/rpcpb"
)

func fieldNumber(m proto.Message, name protoreflect.Name) protowire.Number {
	return m.ProtoReflect().Descriptor().Fields().ByName(name).Number()
}

var (
	verifyTimeoutDiffs    = fieldNumber(&rpcpb.VerifyTimeoutRequest{}, "diffs")
	verifyErrorMissDiffs  = fieldNumber(&rpcpb.VerifyErrorMissRequest{}, "diffs")
	challengeReceiptDiffs = fieldNumber(&rpcpb.ChallengeReceiptRequest{}, "diffs")
	gossipTxsTxs          = fieldNumber(&rpcpb.GossipTxsRequest{}, "txs")
)

// checkDiffsPayload and checkTxsPayload run before unmarshalPayload, so an
// over-count request is refused before its objects are allocated.
func checkDiffsPayload(payload []byte, field protowire.Number) error {
	if err := transport.CheckDiffsPayload(payload, field); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

func checkTxsPayload(payload []byte, field protowire.Number) error {
	if err := transport.CheckTxsPayload(payload, field); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}
