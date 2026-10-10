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

func finishTxFor(t *testing.T, signer signing.Signer, inferenceID uint64, slot uint32, responseHash []byte) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId: inferenceID, EscrowId: "escrow-1", ExecutorSlot: slot,
		ResponseHash: responseHash, ServedHash: testutil.TestServedHash,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

func confirmTxFor(t *testing.T, signer signing.Signer, inferenceID uint64, rec *types.InferenceRecord, confirmedAt int64) *types.DevshardTx {
	t.Helper()
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID,
		ExecutorSig: testutil.SignExecutorReceipt(t, signer, "escrow-1", inferenceID, rec.PromptHash, rec.Model,
			rec.InputLength, rec.MaxTokens, rec.StartedAt, confirmedAt),
		ConfirmedAt: confirmedAt,
	}}}
}

func pendingFinishFor(s *Session, inferenceID uint64) *types.DevshardTx {
	for _, tx := range s.PendingTxs() {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == inferenceID {
			return tx
		}
	}
	return nil
}

// A gossiped finish closes its record on chain, so leaving it unfinished here
// raises a rejected timeout. A finish that cannot apply does not close it:
// the timeout must still run.
func TestAGossipedFinishMarksItsOwnNonceFinished(t *testing.T) {
	session, hosts, _ := setupSession(t, 1, 1000000, 100)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 10, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	var prepared *PreparedInference
	for range 11 {
		p, err := session.PrepareInference(params)
		require.NoError(t, err)
		prepared = p
	}
	require.Equal(t, uint64(11), prepared.Nonce())
	slot, ok := session.StateMachine().InferenceExecutorSlot(11)
	require.True(t, ok)
	respond := func(txs ...*types.DevshardTx) {
		t.Helper()
		require.NoError(t, session.ProcessResponse(0, &host.HostResponse{Mempool: txs}, 7))
	}

	respond(&types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: 11,
	}}})
	require.False(t, session.IsNonceFinished(11), "a finish with only an inference id must not skip the timeout")
	require.False(t, session.IsNonceFinished(7))

	short := finishTxFor(t, hosts[0], 11, slot, []byte("short"))
	respond(short)
	require.False(t, session.IsNonceFinished(11), "a signed finish apply would reject must not skip the timeout")

	valid := finishTxFor(t, hosts[0], 11, slot, testutil.TestResponseHash)
	respond(valid)
	require.False(t, session.IsNonceFinished(11),
		"the record is still pending and no ConfirmStart is queued, so this finish cannot apply")
	require.True(t, proto.Equal(short, pendingFinishFor(session, 11)), "neither copy can apply yet, so the first one keeps its slot")

	rec, ok := session.StateMachine().Inference(11)
	require.True(t, ok)
	respond(confirmTxFor(t, hosts[0], 11, rec, 2000), valid)
	require.True(t, session.IsNonceFinished(11), "a queued ConfirmStart lets the finish apply, so the timeout round would be raised against a closed record")
	require.True(t, proto.Equal(valid, pendingFinishFor(session, 11)), "the finish that applies replaces the queued one that does not")
	require.False(t, session.IsNonceFinished(7), "nonce 7 was not finished by this mempool, so nothing may mark it")

	require.NoError(t, session.SendPendingDiff(context.Background()))
	finished, ok := session.StateMachine().Inference(11)
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, finished.Status, "what counted as finished is what landed")
}

// With a warm-key resolver, a slot that has not bound a warm key cannot reject
// an unknown signer from state alone. Such a finish must not count as done.
func TestUnboundSlotFinishFromUnknownKeyDoesNotMarkFinished(t *testing.T) {
	session, coldKeys, _ := setupWarmKeySession(t, 3)
	_, err := session.PrepareInference(defaultParams)
	require.NoError(t, err)
	nonce := session.Nonce()
	slot, ok := session.StateMachine().InferenceExecutorSlot(nonce)
	require.True(t, ok)
	_, bound := session.StateMachine().WarmKeys()[slot]
	require.False(t, bound)
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)

	attacker := testutil.MustGenerateKey(t)
	confirm := confirmTxFor(t, coldKeys[slot], nonce, rec, 2000)
	require.NoError(t, session.ProcessResponse(int(slot), &host.HostResponse{Mempool: []*types.DevshardTx{
		confirm, finishTxFor(t, attacker, nonce, slot, testutil.TestResponseHash),
	}}, nonce))
	require.False(t, session.IsNonceFinished(nonce), "a finish from an unknown key on an unbound slot must not skip the timeout")

	require.NoError(t, session.ProcessResponse(int(slot), &host.HostResponse{Mempool: []*types.DevshardTx{
		finishTxFor(t, coldKeys[slot], nonce, slot, testutil.TestResponseHash),
	}}, nonce))
	require.True(t, session.IsNonceFinished(nonce), "the executor's cold key decides from state alone")
}

// The gateway runs the verifier's checks with the resolver off, so a warm key
// the slot has not bound does not close the nonce and does not query the chain.
// The timeout vote is what asks the resolver.
func TestUnboundWarmKeyFinishDoesNotMarkFinished(t *testing.T) {
	session, coldKeys, warmKeys := setupWarmKeySession(t, 3)
	_, err := session.PrepareInference(defaultParams)
	require.NoError(t, err)
	nonce := session.Nonce()
	slot, ok := session.StateMachine().InferenceExecutorSlot(nonce)
	require.True(t, ok)
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)

	require.NoError(t, session.ProcessResponse(int(slot), &host.HostResponse{Mempool: []*types.DevshardTx{
		confirmTxFor(t, coldKeys[slot], nonce, rec, 2000),
		finishTxFor(t, warmKeys[slot], nonce, slot, testutil.TestResponseHash),
	}}, nonce))
	require.False(t, session.IsNonceFinished(nonce))
}
