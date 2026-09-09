package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestLogPlane_DeterminismAcrossVerifiers(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	hash := []byte{0xaa, 0xbb}

	applySeq := func() (*StateMachine, *heightsync.SyncTurnRecord, []heightsync.AttributableMark) {
		t.Helper()
		group := testutil.MakeGroup(hosts)
		config := testutil.DefaultConfig(len(hosts))
		store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000)
		sm, err := NewStateMachine("escrow-1", config, group, 100000, user.Address(), signing.NewSecp256k1Verifier(), store)
		require.NoError(t, err)

		hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 3, Reason: "quiet_session",
		}}}
		d1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{hb})
		_, err = sm.ApplyDiff(d1)
		require.NoError(t, err)

		ack0 := &types.MsgHeightAck{
			RefNonce: 1, SlotId: 0, ObservedHeight: 50, ObservedBlockHash: hash,
			SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
		}
		ack1 := &types.MsgHeightAck{
			RefNonce: 1, SlotId: 1, ObservedHeight: 50, ObservedBlockHash: hash,
			SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
		}
		require.NoError(t, heightsync.SignAck(hosts[0], ack0))
		require.NoError(t, heightsync.SignAck(hosts[1], ack1))
		d2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
			{Tx: &types.DevshardTx_HeightAck{HeightAck: ack0}},
			{Tx: &types.DevshardTx_HeightAck{HeightAck: ack1}},
		})
		_, err = sm.ApplyDiff(d2)
		require.NoError(t, err)
		return sm, sm.HeightSyncTurnRecord(1), sm.HeightSyncMarks()
	}

	_, recA, marksA := applySeq()
	_, recB, marksB := applySeq()
	require.NotNil(t, recA)
	require.Equal(t, recA, recB)
	require.Equal(t, heightsync.TurnComplete, recA.State)
	require.Equal(t, recA.HReq, recB.HReq)
	require.Equal(t, marksA, marksB)
}

func TestLogPlane_ApplyDiff_FabricatedAckRejected(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 100000)
	hash := []byte{0xaa}
	hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 3, Reason: "quiet_session",
	}}}
	_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{hb}))
	require.NoError(t, err)

	ack := &types.MsgHeightAck{
		RefNonce: 1, SlotId: 0, ObservedHeight: 50, ObservedBlockHash: hash,
		SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(hosts[0], ack))
	ack.HostSig[0] ^= 0xff
	_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
		{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
	}))
	require.ErrorIs(t, err, heightsync.ErrAckSigInvalid)
	require.Equal(t, uint64(1), sm.LatestNonce())
}
