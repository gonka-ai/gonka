package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// prepareNonce consumes one nonce so the session tracks an outcome for it, which is what
// TimeoutDeadline reads.
func prepareNonce(t *testing.T, session *Session) uint64 {
	t.Helper()
	prepared, err := session.PrepareInference(context.Background(), InferenceParams{
		Prompt:      []byte(`{"messages":[{"role":"user","content":"x"}]}`),
		Model:       "llama",
		InputLength: 1,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   time.Now().Unix(),
	})
	require.NoError(t, err)
	return prepared.Nonce()
}

// signedExecutorReceipt is the assigned executor's signature over the live record at confirmedAt.
func signedExecutorReceipt(t *testing.T, session *Session, hosts []*signing.Secp256k1Signer, nonce uint64, confirmedAt int64) []byte {
	t.Helper()
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	require.Less(t, int(rec.ExecutorSlot), len(hosts))
	return testutil.SignExecutorReceipt(t, hosts[rec.ExecutorSlot], session.escrowID, nonce,
		rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt, confirmedAt)
}

// A confirmation timestamp only becomes chain state through the receipt that signs it. Without the
// receipt no MsgConfirmStart is ever queued, so the record stays pending and an execution timeout —
// the one this timestamp would select — is a vote every verifier has to reject. Believing the
// timestamp alone lets a host bury each of its nonces in a round that can never pass.
func TestProcessResponse_AConfirmationWithoutItsReceiptIsNotBelieved(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce:       nonce,
		ConfirmedAt: time.Now().Unix(),
	}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "refused", reason,
		"an unproven confirmation must leave the nonce answerable as a refusal")
}

// Bytes that are not the executor's signature are not a confirmation. The chain rejects them,
// the record stays pending, and a refusal is still the vote that can record the miss.
func TestProcessResponse_AnUnverifiedReceiptIsNotBelieved(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce:       nonce,
		Receipt:     []byte("executor-signature"),
		ConfirmedAt: time.Now().Unix(),
	}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "refused", reason,
		"an unverified receipt must leave the nonce answerable as a refusal")
	require.Empty(t, pendingConfirmStarts(session), "a receipt the chain would reject must not be queued")
}

// The assigned executor's signature is what the chain checks, so with one present the confirmation counts.
func TestProcessResponse_AnExecutorSignedReceiptIsBelieved(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	confirmedAt := time.Now().Unix()

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce:       nonce,
		Receipt:     signedExecutorReceipt(t, session, hosts, nonce, confirmedAt),
		ConfirmedAt: confirmedAt,
	}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
}

// A signature from a different slot is not the assigned executor, so it must not cancel the refusal.
func TestProcessResponse_AReceiptFromAnotherSlotIsNotBelieved(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	rec, ok := session.sm.GetInference(nonce)
	require.True(t, ok)
	other := hosts[(int(rec.ExecutorSlot)+1)%len(hosts)]
	confirmedAt := time.Now().Unix()
	receipt := testutil.SignExecutorReceipt(t, other, session.escrowID, nonce,
		rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt, confirmedAt)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce: nonce, Receipt: receipt, ConfirmedAt: confirmedAt,
	}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "refused", reason)
	require.Empty(t, pendingConfirmStarts(session))
}

// A forged confirm in the mempool shares the dedup key with the receipt. It must not be the message
// that gets queued, and it must not be what cancels the refusal.
func TestProcessResponse_AForgedMempoolConfirmCannotShadowTheReceipt(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	confirmedAt := time.Now().Unix()
	receipt := signedExecutorReceipt(t, session, hosts, nonce, confirmedAt)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce: nonce, Receipt: receipt, ConfirmedAt: confirmedAt,
		Mempool: []*types.DevshardTx{{
			Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: nonce, ExecutorSig: []byte("forged"), ConfirmedAt: confirmedAt,
			}},
		}},
	}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
	pending := pendingConfirmStarts(session)
	require.Equal(t, []uint64{nonce}, pending)
	var queued []byte
	for _, tx := range session.PendingTxs() {
		if confirm := tx.GetConfirmStart(); confirm != nil && confirm.InferenceId == nonce {
			queued = confirm.ExecutorSig
		}
	}
	require.Equal(t, receipt, queued)
}

func TestProcessResponse_AForgedMempoolConfirmIsNotBelieved(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce: nonce,
		Mempool: []*types.DevshardTx{{
			Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: nonce, ExecutorSig: []byte("forged"), ConfirmedAt: time.Now().Unix(),
			}},
		}},
	}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "refused", reason)
	require.Empty(t, pendingConfirmStarts(session))
}

// A host that reports neither is simply one that has not answered yet.
func TestProcessResponse_NoConfirmationLeavesTheNonceRefusable(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{Nonce: nonce}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "refused", reason)
}
