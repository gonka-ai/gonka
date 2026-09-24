package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func applyFinishWithServedHash(t *testing.T, servedHash []byte, mutateAfterSigning func(*types.MsgFinishInference)) (*StateMachine, error) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	stateMachine, user := newTestSM(t, hosts, 10000)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})})
	_, err := stateMachine.ApplyDiff(diff)
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	diff = testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	})})
	_, err = stateMachine.ApplyDiff(diff)
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"), ServedHash: servedHash,
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	if mutateAfterSigning != nil {
		mutateAfterSigning(finishMsg)
	}
	diff = testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinish(finishMsg)})
	_, err = stateMachine.ApplyDiff(diff)
	return stateMachine, err
}

// Test flow:
//  1. Start, confirm and finish an inference whose Finish carries a served hash.
//  2. Assert the inference record keeps that served hash.
func TestFinishRecordsTheServedHash(t *testing.T) {
	stateMachine, err := applyFinishWithServedHash(t, []byte("served"), nil)
	require.NoError(t, err)

	record := stateMachine.SnapshotState().Inferences[1]
	require.Equal(t, []byte("served"), record.ServedHash)
}

// Test flow:
//  1. Sign a Finish, then change its served hash.
//  2. Assert applying it fails the proposer signature.
func TestTheProposerSignatureCoversTheServedHash(t *testing.T) {
	_, err := applyFinishWithServedHash(t, []byte("served"), func(finishMsg *types.MsgFinishInference) {
		finishMsg.ServedHash = []byte("another view")
	})
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)
}

// Test flow:
//  1. Marshal the same record with and without a served hash.
//  2. Assert the entries differ, so the state root commits to it.
//  3. Unmarshal the entry and assert the served hash comes back.
func TestTheServedHashEntersTheInferenceEntry(t *testing.T) {
	record := &types.InferenceRecord{Status: types.StatusFinished, ResponseHash: []byte("response"), ServedHash: []byte("served")}
	entry, err := marshalInferenceEntry(1, record)
	require.NoError(t, err)
	withoutServed, err := marshalInferenceEntry(1, &types.InferenceRecord{Status: types.StatusFinished, ResponseHash: []byte("response")})
	require.NoError(t, err)
	require.NotEqual(t, entry, withoutServed, "the state root commits to the served hash")

	_, decoded, err := unmarshalInferenceEntry(entry)
	require.NoError(t, err)
	require.Equal(t, []byte("served"), decoded.ServedHash)
}

// Test flow:
//  1. Finish an inference with a served hash and snapshot the state.
//  2. Marshal and unmarshal the snapshot.
//  3. Assert the restored record keeps the served hash.
func TestTheServedHashSurvivesASnapshot(t *testing.T) {
	stateMachine, err := applyFinishWithServedHash(t, []byte("served"), nil)
	require.NoError(t, err)
	snapshot := stateMachine.SnapshotState()

	data, err := types.MarshalStateSnapshotProto(&snapshot, nil, nil, nil)
	require.NoError(t, err)
	restored, _, _, _, err := types.UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)
	require.Equal(t, []byte("served"), restored.Inferences[1].ServedHash)
}
