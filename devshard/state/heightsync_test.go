package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestApplyLocalBestEffort_HeartbeatAndAckStayInDiff(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 100000)

	hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq: 1, ObservedHeight: 50, SlotsNum: 3, Reason: "quiet_session",
	}}}
	ack := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
		TurnSeq: 1, RefNonce: 1, SlotId: 0, ObservedHeight: 50, SyncState: types.SyncState_SYNCED,
	}}}
	_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{hb, ack})
	require.NoError(t, err)
	require.Len(t, applied, 2)
	require.NotNil(t, applied[0].GetHeartbeat())
	require.NotNil(t, applied[1].GetHeightAck())
}
