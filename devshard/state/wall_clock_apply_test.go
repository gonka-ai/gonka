package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestApplyStartConfirm_CopiesWallClocks(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	const startedAt int64 = 1_700_000_100
	const confirmedAt int64 = 1_700_000_142

	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens,
		StartedAt: startedAt,
	})}))
	require.NoError(t, err)
	require.Equal(t, startedAt, sm.SnapshotState().Inferences[1].StartedAt)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens,
		testutil.ReceiptStamp{StartedAt: startedAt, ConfirmedAt: confirmedAt})
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	})}))
	require.NoError(t, err)

	rec := sm.SnapshotState().Inferences[1]
	require.Equal(t, types.StatusStarted, rec.Status)
	require.Equal(t, startedAt, rec.StartedAt)
	require.Equal(t, confirmedAt, rec.ConfirmedAt)
}

func TestConfirmReceipt_WallClockTamperFails(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	sm, user := newTestSM(t, hosts, 10000)

	const startedAt int64 = 1_700_000_200
	const confirmedAt int64 = 1_700_000_250

	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{txStart(&types.MsgStartInference{
		InferenceId: 1, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens,
		StartedAt: startedAt,
	})}))
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, []byte("prompt"), "llama", 100, testutil.TestMaxTokens,
		testutil.ReceiptStamp{StartedAt: startedAt, ConfirmedAt: confirmedAt})

	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: confirmedAt + 1,
	})}))
	require.ErrorIs(t, err, types.ErrInvalidExecutorSig)

	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{txConfirm(&types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: confirmedAt,
	})}))
	require.NoError(t, err)
}

func TestMarshalInferenceEntry_WallInRootStampTimeMsOut(t *testing.T) {
	base := &types.InferenceRecord{
		Status:             types.StatusStarted,
		ExecutorSlot:       1,
		Model:              "llama",
		PromptHash:         []byte("prompt"),
		InputLength:        100,
		MaxTokens:          testutil.TestMaxTokens,
		ReservedCost:       100,
		StartedAt:          1_700_000_300,
		ConfirmedAt:        1_700_000_310,
		StartedAtHeight:    100,
		StartedAtBlockHash: []byte{0xaa},
		ConfirmedAtHeight:  101,
	}
	withStampTimes := *base
	withStampTimes.StartedAtTimeMs = 111
	withStampTimes.ConfirmedAtTimeMs = 222

	a, err := marshalInferenceEntry(1, base)
	require.NoError(t, err)
	b, err := marshalInferenceEntry(1, &withStampTimes)
	require.NoError(t, err)
	require.Equal(t, a, b, "stamp *TimeMs must stay out of the state-root entry")

	otherWall := *base
	otherWall.StartedAt = base.StartedAt + 1
	c, err := marshalInferenceEntry(1, &otherWall)
	require.NoError(t, err)
	require.NotEqual(t, a, c, "wall StartedAt must change the state-root entry")

	_, decoded, err := unmarshalInferenceEntry(a)
	require.NoError(t, err)
	require.Equal(t, base.StartedAt, decoded.StartedAt)
	require.Equal(t, base.ConfirmedAt, decoded.ConfirmedAt)
	require.Zero(t, decoded.StartedAtTimeMs)
	require.Zero(t, decoded.ConfirmedAtTimeMs)
}
