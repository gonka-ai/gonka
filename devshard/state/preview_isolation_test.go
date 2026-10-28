package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func startInferenceTx(inferenceID uint64, prompt string) *types.DevshardTx {
	return txStart(&types.MsgStartInference{
		InferenceId: inferenceID, PromptHash: []byte(prompt), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
}

// Test flow:
//  1. Start inference 1 on a live machine and record its root and balance.
//  2. Preview nonce 2, which starts inference 2, and keep the handle uncommitted.
//  3. Assert the live machine still holds only inference 1 at the recorded root and balance.
//  4. Commit the handle and assert the machine holds both inferences at the previewed root.
func TestAPreviewLeavesTheLiveStateUntouchedUntilItsHandleIsCommitted(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _ := newTestSM(t, hosts, 10000)
	_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{startInferenceTx(1, "prompt")})
	require.NoError(t, err)
	rootBefore, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	balanceBefore := sm.SnapshotState().Balance

	handle, err := sm.PreviewLocalBestEffort(2, []*types.DevshardTx{startInferenceTx(2, "prompt")})
	require.NoError(t, err)

	rootBetween, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootBetween, "the live root moved before the handle was committed")
	between := sm.SnapshotState()
	require.Len(t, between.Inferences, 1, "the previewed inference leaked into the live state")
	require.Equal(t, balanceBefore, between.Balance, "the previewed reservation leaked into the live balance")

	require.True(t, sm.CommitValidated(handle))
	rootAfter, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, handle.Root, rootAfter)
	require.Len(t, sm.SnapshotState().Inferences, 2)
}

// Test flow:
//  1. Start inference 1, then preview nonce 2 and throw the handle away.
//  2. Apply a different nonce 2 that starts inference 2 with another prompt.
//  3. Assert the applied record carries the second prompt, so nothing of the discarded preview survived.
func TestADiscardedPreviewLeavesNothingBehindForTheNextApply(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _ := newTestSM(t, hosts, 10000)
	_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{startInferenceTx(1, "prompt")})
	require.NoError(t, err)
	_, err = sm.PreviewLocalBestEffort(2, []*types.DevshardTx{startInferenceTx(2, "prompt")})
	require.NoError(t, err)

	_, applied, err := sm.ApplyLocalBestEffort(2, []*types.DevshardTx{startInferenceTx(2, "another prompt")})
	require.NoError(t, err)
	require.Len(t, applied, 1)

	record := sm.SnapshotState().Inferences[2]
	require.NotNil(t, record)
	require.Equal(t, []byte("another prompt"), record.PromptHash)
}
