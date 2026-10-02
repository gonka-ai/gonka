package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestVerifyConfirmStart(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	good := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	require.NoError(t, sm.VerifyConfirmStart(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: good, ConfirmedAt: 1000}))

	other := testutil.SignExecutorReceipt(t, hosts[2], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	require.Error(t, sm.VerifyConfirmStart(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: other, ConfirmedAt: 1000}))
	require.Error(t, sm.VerifyConfirmStart(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte{0}, ConfirmedAt: 1000}))
	require.Error(t, sm.VerifyConfirmStart(&types.MsgConfirmStart{InferenceId: 2, ExecutorSig: good, ConfirmedAt: 1000}))

	// Verification must not apply: record stays Pending.
	require.Equal(t, types.StatusPending, sm.SnapshotState().Inferences[1].Status)
}
