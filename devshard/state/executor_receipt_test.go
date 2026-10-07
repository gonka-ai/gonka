package state

import (
	"testing"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestExecutorReceiptCanonicalDuplicateSurvivesFloorAdvance(t *testing.T) {
	signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, _ := newTestSM(t, signers, 100000)
	_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 1, Model: "llama", PromptHash: testutil.TestPromptHash[:], InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}}}})
	require.NoError(t, err)
	receipt := &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: 1000, ObservedHeight: 10, ObservedBlockHash: []byte{0xaa}}
	receipt.ExecutorSig = testutil.SignExecutorReceipt(t, signers[1], "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens, 1000, 1000, testutil.ReceiptStamp{Height: 10, Hash: receipt.ObservedBlockHash})
	_, _, err = sm.ApplyLocalBestEffort(2, []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: receipt}}})
	require.NoError(t, err)
	rec, ok := sm.Inference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	_, _, err = sm.ApplyLocalBestEffort(3, []*types.DevshardTx{{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		ObservedHeight: 20, ObservedBlockHash: []byte{0xbb}, SlotsNum: 3,
	}}}})
	require.NoError(t, err)
	ack := &types.MsgHeightAck{RefNonce: 3, SlotId: 0, ObservedHeight: 20, ObservedBlockHash: []byte{0xbb}, SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff}}
	require.NoError(t, heightsync.SignAck(signers[0], ack))
	_, _, err = sm.ApplyLocalBestEffort(4, []*types.DevshardTx{{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}})
	require.NoError(t, err)
	floor, _, known := sm.HeightSyncFloorAsOf(5)
	require.True(t, known)
	require.Equal(t, uint64(20), floor)
	before := sm.SnapshotState()
	require.NoError(t, sm.VerifyExecutorReceipt(receipt))
	after := sm.SnapshotState()
	require.Equal(t, before, after, "authentication must remain mutation-free")
	bad := *receipt
	bad.ExecutorSig = []byte("garbage")
	require.ErrorIs(t, sm.VerifyExecutorReceipt(&bad), types.ErrInvalidExecutorSig)
	bad = *receipt
	bad.ObservedHeight++
	require.ErrorIs(t, sm.VerifyExecutorReceipt(&bad), types.ErrInvalidTransition)
}
