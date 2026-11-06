package rpcserver

import (
	"bytes"
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/transport"
	"devshard/transport/rpcpb"
)

func TestSessionHandler_RefusesOverCountDiffsBeforeDecode(t *testing.T) {
	env := newSessionEnv(t, stubLookup{core: stubCore{member: true}}, "escrow-1")
	call := func(diffs []*rpcpb.Diff) error {
		_, err := env.session.VerifyTimeout(context.Background(), withSession(
			connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.VerifyTimeoutRequest{Diffs: diffs})), env.token))
		return err
	}
	tooMany := make([]*rpcpb.Diff, transport.MaxDecodedDiffs+1)
	for i := range tooMany {
		tooMany[i] = &rpcpb.Diff{}
	}
	err := call(tooMany)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "decode limit")

	bomb := bytes.Repeat([]byte{0x12, 0x00}, 65537) // empty txs
	err = call([]*rpcpb.Diff{{Nonce: 1, Txs: bomb}})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "decode limit")

	_, err = env.session.VerifyErrorMiss(context.Background(), withSession(
		connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.VerifyErrorMissRequest{Diffs: tooMany})), env.token))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = env.session.ChallengeReceipt(context.Background(), withSession(
		connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.ChallengeReceiptRequest{Diffs: tooMany})), env.token))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	require.NoError(t, call(tooMany[:1]))
}
