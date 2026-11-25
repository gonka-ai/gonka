package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"devshard/user"
)

const finishSourceEscrowID = "escrow-finish-source"

func sessionQueuingFinish(t *testing.T, nonce uint64) (*user.Session, *signing.Secp256k1Signer, *types.DevshardTx) {
	t.Helper()
	signer := testutil.MustGenerateKey(t)
	executor := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{executor})
	verifier := signing.NewSecp256k1Verifier()
	sm := statetest.MustStateMachine(t, finishSourceEscrowID, types.SessionConfig{VoteThreshold: 1}, group, 1000, signer.Address(), verifier)
	st := sm.ExportState()
	st.Inferences = map[uint64]*types.InferenceRecord{nonce: {Status: types.StatusStarted}}
	require.NoError(t, sm.RestoreState(st))
	session, err := user.NewSession(sm, signer, finishSourceEscrowID, group, []user.HostClient{&user.InProcessClient{}}, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	queued := signedFinish(t, executor, finishSourceEscrowID, nonce, 1)
	require.NoError(t, session.ProcessResponse(0, &host.HostResponse{Mempool: []*types.DevshardTx{queued}}, nonce))
	require.NotEmpty(t, session.FinishTxFor(nonce), "the fixture must queue the gossiped Finish")
	return session, executor, queued
}

func signedFinish(t *testing.T, executor *signing.Secp256k1Signer, escrowID string, nonce, outputTokens uint64) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{InferenceId: nonce, EscrowId: escrowID, OutputTokens: outputTokens, ResponseHash: testutil.TestServedHash, ServedHash: testutil.TestServedHash}
	msg.ProposerSig = testutil.SignProposerTx(t, executor, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

// Test flow:
//  1. Build a session that queued one Finish for a started nonce from a gossiped reply.
//  2. Build an error attempt whose own reply carries a different Finish the same executor signed, and one whose reply carries none.
//  3. Assert the claim built for the first carries its reply's Finish, not the queued one, and the claim built for the second carries the queued one.
func TestErrorMissArtifacts_FinishFromTheReplyBeforeTheSession(t *testing.T) {
	const nonce = uint64(3)
	session, executor, queued := sessionQueuingFinish(t, nonce)
	own := signedFinish(t, executor, finishSourceEscrowID, nonce, 2)
	withOwn := &inflight{nonce: nonce, errorTerminal: true, resp: &host.HostResponse{Mempool: []*types.DevshardTx{own}}}
	withoutOwn := &inflight{nonce: nonce, errorTerminal: true, resp: &host.HostResponse{}}

	fromReply, _ := errorMissArtifacts(withOwn, session)
	fromSession, _ := errorMissArtifacts(withoutOwn, session)

	require.Equal(t, user.MarshalFinishTx([]*types.DevshardTx{own}, nonce), fromReply)
	require.NotEqual(t, session.FinishTxFor(nonce), fromReply)
	require.Equal(t, user.MarshalFinishTx([]*types.DevshardTx{queued}, nonce), fromSession)
}
