package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestConfirmStart_TamperedObservedHeightFailsExecutorSig(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)
	hash := []byte{0xaa}

	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		ObservedHeight: 100, ObservedBlockHash: hash,
	})}))
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000,
		testutil.ReceiptStamp{Height: 100, Hash: hash})
	msg := &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
		ObservedHeight: 100, ObservedBlockHash: hash,
	}
	msg.ObservedHeight = 101
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(msg)}))
	require.ErrorIs(t, err, types.ErrInvalidExecutorSig)
}

func TestFinishInference_StampCoveredByProposerSig(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)
	hash := []byte{0xaa}

	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		ObservedHeight: 100, ObservedBlockHash: hash,
	})}))
	require.NoError(t, err)
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000,
		testutil.ReceiptStamp{Height: 100, Hash: hash})
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
		ObservedHeight: 100, ObservedBlockHash: hash,
	})}))
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: 1,
		EscrowId: "escrow-1", ObservedHeight: 100, ObservedBlockHash: hash,
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)

	tampered := protoCloneFinish(finishMsg)
	tampered.ObservedBlockHash = []byte{0xbb}
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{txFinish(tampered)}))
	require.ErrorIs(t, err, types.ErrInvalidProposerSig)

	require.NoError(t, applyFinish(t, sm, user, 3, protoCloneFinish(finishMsg)))
}

func TestApply_RecordCarriesStampHeights(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	hash := []byte{0xaa}

	stamped, userS := newTestSM(t, hosts, 10000)
	plain, userP := newTestSM(t, hosts, 10000)

	start := &types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := plain.ApplyDiff(testutil.SignDiff(t, userP, "escrow-1", 1, []*types.DevshardTx{txStart(start)}))
	require.NoError(t, err)
	stampedStart := proto.Clone(start).(*types.MsgStartInference)
	stampedStart.ObservedHeight = 100
	stampedStart.ObservedBlockHash = hash
	_, err = stamped.ApplyDiff(testutil.SignDiff(t, userS, "escrow-1", 1, []*types.DevshardTx{txStart(stampedStart)}))
	require.NoError(t, err)

	plainSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000)
	stampSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000,
		testutil.ReceiptStamp{Height: 101, Hash: hash})
	_, err = plain.ApplyDiff(testutil.SignDiff(t, userP, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: plainSig, ConfirmedAt: 1000,
	})}))
	require.NoError(t, err)
	_, err = stamped.ApplyDiff(testutil.SignDiff(t, userS, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: stampSig, ConfirmedAt: 1000,
		ObservedHeight: 101, ObservedBlockHash: hash,
	})}))
	require.NoError(t, err)

	rec := stamped.SnapshotState().Inferences[1]
	require.Equal(t, uint64(100), rec.StartedAtHeight)
	require.Equal(t, uint64(101), rec.ConfirmedAtHeight)

	stampedRoot, err := stamped.ComputeStateRoot()
	require.NoError(t, err)
	plainRoot, err := plain.ComputeStateRoot()
	require.NoError(t, err)
	require.NotEqual(t, plainRoot, stampedRoot, "stamped record changes post_state_root")
}

func protoCloneFinish(msg *types.MsgFinishInference) *types.MsgFinishInference {
	return proto.Clone(msg).(*types.MsgFinishInference)
}

func applyFinish(t *testing.T, sm *StateMachine, user *signing.Secp256k1Signer, nonce uint64, msg *types.MsgFinishInference) error {
	t.Helper()
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{txFinish(msg)}))
	return err
}
