package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// chainedDiffs builds start → confirm → finish for inference 1, each signed
// with the root a fresh SM reaches, so every diff depends on the one before.
func chainedDiffs(t *testing.T, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, probe *StateMachine) []types.Diff {
	t.Helper()
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens)
	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1, EscrowId: "escrow-1",
	}
	finish.ProposerSig = testutil.SignProposerTx(t, hosts[1], finish)
	txsByNonce := [][]*types.DevshardTx{
		{txStart(&types.MsgStartInference{
			InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
			InputLength: 100, MaxTokens: testutil.TestMaxTokens,
		})},
		{txConfirm(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: execSig})},
		{txFinish(finish)},
	}
	diffs := make([]types.Diff, 0, len(txsByNonce))
	for i, txs := range txsByNonce {
		nonce := uint64(i + 1)
		root, err := probe.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		diffs = append(diffs, testutil.SignDiffWithRoot(t, user, "escrow-1", nonce, txs, root))
	}
	return diffs
}

func newChainSMs(t *testing.T, n int) (hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, sms []*StateMachine) {
	t.Helper()
	hosts = []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user = testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	for range n {
		store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000)
		sm, err := NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
		require.NoError(t, err)
		sms = append(sms, sm)
	}
	return hosts, user, sms
}

// Test flow:
//  1. Validate a dependent run of three diffs; the live state does not move.
//  2. Commit the handles in order.
//  3. The result equals applying the same diffs one by one.
func TestValidateDiffs_ChainCommitEqualsSequentialApply(t *testing.T) {
	hosts, user, sms := newChainSMs(t, 3)
	probe, batched, sequential := sms[0], sms[1], sms[2]
	diffs := chainedDiffs(t, hosts, user, probe)

	rootBefore, err := batched.ComputeStateRoot()
	require.NoError(t, err)
	vds, err := batched.ValidateDiffs(diffs)
	require.NoError(t, err)
	require.Len(t, vds, 3)
	require.Zero(t, batched.LatestNonce(), "validation commits nothing")
	rootAfterValidate, err := batched.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfterValidate)
	require.Empty(t, batched.SnapshotState().Inferences)

	for i, vd := range vds {
		require.Equal(t, diffs[i].PostStateRoot, vd.Root)
		require.True(t, batched.CommitValidated(vd))
	}
	for _, d := range diffs {
		_, err := sequential.ApplyDiff(d)
		require.NoError(t, err)
	}

	require.Equal(t, uint64(3), batched.LatestNonce())
	got, err := batched.ComputeStateRoot()
	require.NoError(t, err)
	want, err := sequential.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, types.StatusFinished, batched.SnapshotState().Inferences[1].Status)
}

func TestValidateDiffs_StopsAtFirstInvalidDiff(t *testing.T) {
	hosts, user, sms := newChainSMs(t, 2)
	probe, sm := sms[0], sms[1]
	diffs := chainedDiffs(t, hosts, user, probe)
	diffs[2] = testutil.SignDiffWithRoot(t, user, "escrow-1", 3, diffs[2].Txs, []byte("wrong-root"))

	vds, err := sm.ValidateDiffs(diffs)
	require.Error(t, err)
	require.Len(t, vds, 2, "the valid prefix is returned")
	require.Zero(t, sm.LatestNonce())
	require.Empty(t, sm.SnapshotState().Inferences, "the trial of the valid prefix is undone too")

	for _, vd := range vds {
		require.True(t, sm.CommitValidated(vd))
	}
	require.Equal(t, uint64(2), sm.LatestNonce())
	require.Equal(t, types.StatusStarted, sm.SnapshotState().Inferences[1].Status)
}

func TestValidateDiffs_BadSignatureTruncatesRun(t *testing.T) {
	hosts, user, sms := newChainSMs(t, 2)
	probe, sm := sms[0], sms[1]
	diffs := chainedDiffs(t, hosts, user, probe)
	diffs[1].UserSig = append([]byte(nil), diffs[0].UserSig...)

	vds, err := sm.ValidateDiffs(diffs)
	require.ErrorIs(t, err, types.ErrInvalidUserSig)
	require.Len(t, vds, 1)
	require.Zero(t, sm.LatestNonce())
}
