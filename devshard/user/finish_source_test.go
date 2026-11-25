package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

const finishSourceHosts = 3

func startedErrorFinish(t *testing.T) (*Session, []*signing.Secp256k1Signer, uint64, int, *types.DevshardTx) {
	t.Helper()
	session, hosts, _ := setupSession(t, finishSourceHosts, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	nonce, execIdx := startConfirmForErrorMiss(t, session, hosts, params)
	finishTx, _ := signedErrorFinishTx(t, hosts, nonce, execIdx, errorMissPayload(t))
	return session, hosts, nonce, execIdx, finishTx
}

func resignedFinish(t *testing.T, signer *signing.Secp256k1Signer, finishTx *types.DevshardTx, edit func(*types.MsgFinishInference)) *types.DevshardTx {
	t.Helper()
	msg := proto.Clone(finishTx.GetFinishInference()).(*types.MsgFinishInference)
	msg.ProposerSig = nil
	edit(msg)
	msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

// Test flow:
//  1. Start an inference and sign its executor's Finish over an error payload.
//  2. Process the executor's reply carrying that Finish in its mempool.
//  3. Assert the Finish read from the reply's mempool and the one the session queued are the same non-empty bytes.
func TestFinishTxFor_MatchesTheProcessedReply(t *testing.T) {
	session, _, nonce, execIdx, finishTx := startedErrorFinish(t)
	reply := &host.HostResponse{Mempool: []*types.DevshardTx{finishTx}}

	require.NoError(t, session.ProcessResponse(execIdx, reply, nonce))

	fromReply := MarshalFinishTx(reply.Mempool, nonce)
	require.NotEmpty(t, fromReply)
	require.Equal(t, fromReply, session.FinishTxFor(nonce))
}

// Test flow:
//  1. Start an inference and sign its executor's Finish.
//  2. Process another host's reply gossiping a byte-equal copy of that Finish, then the executor's own reply.
//  3. Assert the session's queued Finish equals the executor reply's Finish byte for byte.
func TestFinishTxFor_MatchesTheReplyAfterAnEqualGossipCopy(t *testing.T) {
	session, _, nonce, execIdx, finishTx := startedErrorFinish(t)
	gossip := &host.HostResponse{Mempool: []*types.DevshardTx{proto.Clone(finishTx).(*types.DevshardTx)}}
	reply := &host.HostResponse{Mempool: []*types.DevshardTx{finishTx}}

	require.NoError(t, session.ProcessResponse((execIdx+1)%finishSourceHosts, gossip, nonce))
	require.NoError(t, session.ProcessResponse(execIdx, reply, nonce))

	require.Equal(t, MarshalFinishTx(reply.Mempool, nonce), session.FinishTxFor(nonce))
}

// Test flow:
//  1. Start an inference and have its executor sign two different Finishes for the nonce.
//  2. Process another host's reply gossiping the first, then the executor's reply carrying the second.
//  3. Assert the session keeps the first Finish and the reply's Finish differs from it.
func TestFinishTxFor_KeepsTheFirstOfTwoExecutorFinishes(t *testing.T) {
	session, hosts, nonce, execIdx, finishTx := startedErrorFinish(t)
	first := resignedFinish(t, hosts[execIdx], finishTx, func(msg *types.MsgFinishInference) { msg.OutputTokens = 1 })
	gossip := &host.HostResponse{Mempool: []*types.DevshardTx{first}}
	reply := &host.HostResponse{Mempool: []*types.DevshardTx{finishTx}}

	require.NoError(t, session.ProcessResponse((execIdx+1)%finishSourceHosts, gossip, nonce))
	require.NoError(t, session.ProcessResponse(execIdx, reply, nonce))

	require.Equal(t, MarshalFinishTx(gossip.Mempool, nonce), session.FinishTxFor(nonce))
	require.NotEqual(t, MarshalFinishTx(reply.Mempool, nonce), session.FinishTxFor(nonce))
}

// Test flow:
//  1. Start an inference, process the executor's reply carrying its error Finish, and compose a diff with no pin held, so the Finish applies and the record is finished.
//  2. Assert the session's queued set no longer holds the Finish while the reply's mempool still does.
//  3. Claim the error miss with real verifiers and the reply's Finish.
//  4. Assert the miss lands and the record is timed out.
func TestFinishTxFor_EmptyOnceTheReplyFinishApplied(t *testing.T) {
	session, hosts, nonce, execIdx, finishTx := startedErrorFinish(t)
	reply := &host.HostResponse{Mempool: []*types.DevshardTx{finishTx}}
	require.NoError(t, session.ProcessResponse(execIdx, reply, nonce))
	require.NoError(t, session.SendPendingDiff(context.Background()))
	require.Equal(t, types.StatusFinished, session.StateMachine().SnapshotState().Inferences[nonce].Status)

	fromReply := MarshalFinishTx(reply.Mempool, nonce)
	require.NotEmpty(t, fromReply)
	require.Nil(t, session.FinishTxFor(nonce))

	wrapApplyingErrorMissVerifiers(session, hosts)
	result, err := session.HandleErrorMiss(context.Background(), nonce, fromReply, errorMissPayload(t))

	require.ErrorIs(t, err, ErrInferenceMissed)
	require.True(t, result.Accepted)
	require.Equal(t, types.StatusTimedOut, session.StateMachine().SnapshotState().Inferences[nonce].Status)
}

// Test flow:
//  1. Start an inference and sign its Finish with a key that is not the executor's.
//  2. Process the executor's reply carrying that Finish.
//  3. Assert the session dropped it while the reply's mempool still holds it.
//  4. Process the reply again carrying the executor's own signature and assert the session queues that one.
func TestFinishTxFor_EmptyWhenTheReplyFinishFailsVerification(t *testing.T) {
	session, hosts, nonce, execIdx, finishTx := startedErrorFinish(t)
	forged := resignedFinish(t, hosts[(execIdx+1)%finishSourceHosts], finishTx, func(*types.MsgFinishInference) {})
	reply := &host.HostResponse{Mempool: []*types.DevshardTx{forged}}

	require.NoError(t, session.ProcessResponse(execIdx, reply, nonce))

	require.NotEmpty(t, MarshalFinishTx(reply.Mempool, nonce))
	require.Nil(t, session.FinishTxFor(nonce))
	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{Mempool: []*types.DevshardTx{finishTx}}, nonce))
	require.NotEmpty(t, session.FinishTxFor(nonce))
}

// Test flow:
//  1. Start an inference and have its executor stamp its Finish with a height other than its reply's envelope.
//  2. Process the executor's reply carrying that Finish under the envelope.
//  3. Assert the session omitted it while the reply's mempool still holds it.
//  4. Process it again under an envelope at its own height and assert the session queues it.
func TestFinishTxFor_EmptyWhenTheReplyFinishStampMissesTheEnvelope(t *testing.T) {
	session, hosts, nonce, execIdx, finishTx := startedErrorFinish(t)
	stamped := resignedFinish(t, hosts[execIdx], finishTx, func(msg *types.MsgFinishInference) {
		msg.ObservedHeight = 50
		msg.ObservedBlockHash = []byte("block-50")
	})
	reply := &host.HostResponse{Mempool: []*types.DevshardTx{stamped}, HasEnvelope: true, EnvelopeHeight: 40}

	require.NoError(t, session.ProcessResponse(execIdx, reply, nonce))

	require.NotEmpty(t, MarshalFinishTx(reply.Mempool, nonce))
	require.Nil(t, session.FinishTxFor(nonce))
	require.NoError(t, session.ProcessResponse(execIdx, &host.HostResponse{Mempool: []*types.DevshardTx{stamped}, HasEnvelope: true, EnvelopeHeight: 50}, nonce))
	require.Equal(t, MarshalFinishTx(reply.Mempool, nonce), session.FinishTxFor(nonce))
}

// Test flow:
//  1. Start an inference and sign its executor's Finish.
//  2. Process the executor's reply carrying it but claiming a nonce ahead of the session.
//  3. Assert the reply is refused before its mempool is read: the session queued nothing while the reply's mempool holds the Finish.
func TestFinishTxFor_EmptyWhenTheReplyIsRefusedFirst(t *testing.T) {
	session, _, nonce, execIdx, finishTx := startedErrorFinish(t)
	session.mu.Lock()
	ahead := session.nonce + 1
	session.mu.Unlock()
	reply := &host.HostResponse{Nonce: ahead, Mempool: []*types.DevshardTx{finishTx}}

	require.ErrorIs(t, session.ProcessResponse(execIdx, reply, nonce), ErrHostNonceAhead)

	require.NotEmpty(t, MarshalFinishTx(reply.Mempool, nonce))
	require.Nil(t, session.FinishTxFor(nonce))
}

// Test flow:
//  1. Start an inference and pin its Finish, then process the executor's reply carrying it.
//  2. Compose a diff while the pin is held.
//  3. Assert the session still holds the reply's Finish byte for byte.
func TestFinishTxFor_MatchesTheReplyThroughAPinnedCompose(t *testing.T) {
	session, _, nonce, execIdx, finishTx := startedErrorFinish(t)
	reply := &host.HostResponse{Mempool: []*types.DevshardTx{finishTx}}
	session.PinPendingFinish(nonce)
	defer session.UnpinPendingFinish(nonce)
	require.NoError(t, session.ProcessResponse(execIdx, reply, nonce))

	require.NoError(t, session.SendPendingDiff(context.Background()))

	require.Equal(t, MarshalFinishTx(reply.Mempool, nonce), session.FinishTxFor(nonce))
}
