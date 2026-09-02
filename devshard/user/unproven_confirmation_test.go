package user

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
)

// prepareNonce consumes one nonce so the session tracks an outcome for it, which is what
// TimeoutDeadline reads.
func prepareNonce(t *testing.T, session *Session) uint64 {
	t.Helper()
	prepared, err := session.PrepareInference(InferenceParams{
		Prompt:      []byte(`{"messages":[{"role":"user","content":"x"}]}`),
		Model:       "llama",
		InputLength: 1,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   time.Now().Unix(),
	})
	require.NoError(t, err)
	return prepared.Nonce()
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

// The receipt is what the verifiers can check, so with one present the confirmation counts.
func TestProcessResponse_AConfirmationWithItsReceiptIsBelieved(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{
		Nonce:       nonce,
		Receipt:     []byte("executor-signature"),
		ConfirmedAt: time.Now().Unix(),
	}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "execution", reason)
}

// A host that reports neither is simply one that has not answered yet.
func TestProcessResponse_NoConfirmationLeavesTheNonceRefusable(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)

	require.NoError(t, session.ProcessResponse(int(nonce%3), &host.HostResponse{Nonce: nonce}, nonce))

	reason, _ := session.TimeoutDeadline(nonce, time.Now())
	require.Equal(t, "refused", reason)
}
