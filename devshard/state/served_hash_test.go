package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func applyFinishWithHashes(t *testing.T, responseHash, servedHash []byte, mutateAfterSigning func(*types.MsgFinishInference)) (*StateMachine, error) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	stateMachine, user := newTestSM(t, hosts, 10000)
	executorSlot := applyStartConfirm(t, stateMachine, user, hosts, 1)

	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: responseHash, ServedHash: servedHash,
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: executorSlot,
		EscrowId: "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlot], finishMsg)
	if mutateAfterSigning != nil {
		mutateAfterSigning(finishMsg)
	}
	diff := testutil.SignDiff(t, user, "escrow-1", stateMachine.SnapshotState().LatestNonce+1, []*types.DevshardTx{txFinish(finishMsg)})
	_, err := stateMachine.ApplyDiff(diff)
	return stateMachine, err
}

// Test flow:
//  1. Start, confirm and finish an inference whose Finish carries a served hash.
//  2. Assert the inference record keeps that served hash.
func TestFinishRecordsTheServedHash(t *testing.T) {
	stateMachine, err := applyFinishWithHashes(t, testutil.TestResponseHash, testutil.TestServedHash, nil)
	require.NoError(t, err)

	record := stateMachine.SnapshotState().Inferences[1]
	require.Equal(t, testutil.TestServedHash, record.ServedHash)
}

// Test flow:
//  1. Start and confirm an inference, then finish it with a hash that is empty or not 32 bytes long.
//  2. Assert every such Finish is refused on apply, so no executor escapes the binding by leaving served_hash out.
func TestFinishRefusesAHashThatIsNotASha256(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		responseHash []byte
		servedHash   []byte
	}{
		{name: "no served hash", responseHash: testutil.TestResponseHash},
		{name: "short served hash", responseHash: testutil.TestResponseHash, servedHash: []byte("served")},
		{name: "long served hash", responseHash: testutil.TestResponseHash, servedHash: append(append([]byte(nil), testutil.TestServedHash...), 0)},
		{name: "no response hash", servedHash: testutil.TestServedHash},
		{name: "short response hash", responseHash: []byte("short"), servedHash: testutil.TestServedHash},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := applyFinishWithHashes(t, testCase.responseHash, testCase.servedHash, nil)
			require.ErrorIs(t, err, types.ErrInvalidFinishHash)
		})
	}
}

// Test flow:
//  1. Sign a Finish, then change its served hash.
//  2. Assert applying it fails the proposer signature.
func TestTheProposerSignatureCoversTheServedHash(t *testing.T) {
	_, err := applyFinishWithHashes(t, testutil.TestResponseHash, testutil.TestServedHash, func(finishMsg *types.MsgFinishInference) {
		finishMsg.ServedHash = testutil.TestResponseHash
	})
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)
}

// Test flow:
//  1. Marshal the same record with and without a served hash.
//  2. Assert the entries differ, so the state root commits to it.
//  3. Unmarshal the entry and assert the served hash comes back.
func TestTheServedHashEntersTheInferenceEntry(t *testing.T) {
	record := &types.InferenceRecord{Status: types.StatusFinished, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash}
	entry, err := marshalInferenceEntry(1, record)
	require.NoError(t, err)
	withoutServed, err := marshalInferenceEntry(1, &types.InferenceRecord{Status: types.StatusFinished, ResponseHash: testutil.TestResponseHash})
	require.NoError(t, err)
	require.NotEqual(t, entry, withoutServed, "the state root commits to the served hash")

	_, decoded, err := unmarshalInferenceEntry(entry)
	require.NoError(t, err)
	require.Equal(t, testutil.TestServedHash, decoded.ServedHash)
}

// Test flow:
//  1. Finish an inference with a served hash and snapshot the state.
//  2. Marshal and unmarshal the snapshot.
//  3. Assert the restored record keeps the served hash.
func TestTheServedHashSurvivesASnapshot(t *testing.T) {
	stateMachine, err := applyFinishWithHashes(t, testutil.TestResponseHash, testutil.TestServedHash, nil)
	require.NoError(t, err)
	snapshot := stateMachine.SnapshotState()

	data, err := types.MarshalStateSnapshotProto(&snapshot, nil, nil, nil)
	require.NoError(t, err)
	restored, _, _, _, err := types.UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)
	require.Equal(t, testutil.TestServedHash, restored.Inferences[1].ServedHash)
}
