package state

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/types"
)

func TestExecutorReceiptCanonicalDuplicateSurvivesFloorAdvance(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0xaa}
	d.raise(10, hash)
	id := d.next()
	require.NoError(t, d.start(id, 10, hash))
	confirm := d.confirm(id, 10, hash)
	require.NoError(t, d.apply(confirm))
	receipt := confirm.GetConfirmStart()
	rec, ok := d.sm.Inference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)

	d.raise(20, []byte{0xbb})
	before := d.sm.SnapshotState()
	require.NoError(t, d.sm.VerifyExecutorReceipt(receipt), "F(inference id) is fixed, so the applied receipt stays valid")
	require.Equal(t, before, d.sm.SnapshotState(), "authentication must remain mutation-free")

	bad := proto.Clone(receipt).(*types.MsgConfirmStart)
	bad.ExecutorSig = []byte("garbage")
	require.ErrorIs(t, d.sm.VerifyExecutorReceipt(bad), types.ErrInvalidExecutorSig)
	moved := d.confirm(id, 20, []byte{0xbb}).GetConfirmStart()
	require.ErrorIs(t, d.sm.VerifyExecutorReceipt(moved), types.ErrStampNotFloor)
}
