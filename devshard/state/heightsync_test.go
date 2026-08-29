package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
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
	hash := []byte{0xaa}

	hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq: 1, ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 3, Reason: "quiet_session",
	}}}
	ack := &types.MsgHeightAck{
		TurnSeq: 1, RefNonce: 1, SlotId: 0, ObservedHeight: 50, ObservedBlockHash: hash,
		SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(hosts[0], ack))
	_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{
		hb,
		{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
	})
	require.NoError(t, err)
	require.Len(t, applied, 2)
	require.NotNil(t, applied[0].GetHeartbeat())
	require.NotNil(t, applied[1].GetHeightAck())
}

func TestHeightSyncMissingAcksReportsDegradedTurnThroughSMAPI(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 100000)
	hash := []byte{0xaa}

	appendHeartbeat := func(nonce, turnSeq, observedHeight uint64) {
		t.Helper()
		_, applied, err := sm.ApplyLocalBestEffort(nonce, []*types.DevshardTx{{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq: turnSeq, ObservedHeight: observedHeight, ObservedBlockHash: hash,
				SlotsNum: uint64(len(hosts)), Reason: "quiet_session",
			}},
		}})
		require.NoError(t, err)
		require.Len(t, applied, 1)
	}

	for nonce := uint64(1); nonce <= uint64(len(hosts)); nonce++ {
		appendHeartbeat(nonce, 1, 500)
	}
	rec := sm.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnOpen, rec.State)
	require.Empty(t, sm.HeightSyncMissingAcks(1), "missing slots are gated until the ack window closes")

	afterDeadline := uint64(500) + heightsync.DefaultHeartbeatConfig().AckDeadlineBlocks + 1
	appendHeartbeat(uint64(len(hosts))+1, 2, afterDeadline)

	rec = sm.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnDegraded, rec.State)
	require.Empty(t, rec.Acks)
	require.ElementsMatch(t, []uint32{0, 1, 2, 3}, sm.HeightSyncMissingAcks(1))

	due := sm.HeightSyncRepairDue()
	require.Len(t, due, 1)
	require.Equal(t, uint64(1), due[0].TurnSeq)
	require.Equal(t, uint64(1), due[0].SpanStart)
	require.ElementsMatch(t, []uint32{0, 1, 2, 3}, due[0].Missing)
}

func TestApplyLocalBestEffort_LogPlaneInvalidFailsBeforeNonce(t *testing.T) {
	// L0 / L1 / L2 invalid height-sync txs do not consume the nonce.
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	hash := []byte{0xaa}

	t.Run("L0", func(t *testing.T) {
		sm, _ := newTestSM(t, hosts, 100000)
		_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq: 1, ObservedHeight: 80, ObservedBlockHash: hash, SlotsNum: 3,
			}},
		}})
		require.NoError(t, err)
		require.Equal(t, uint64(1), sm.LatestNonce())

		// Sequencer stamps do not raise F. Seed the floor with a host ack, then
		// a heartbeat below that floor is L0-invalid.
		ack := &types.MsgHeightAck{
			TurnSeq: 1, RefNonce: 1, SlotId: 0, ObservedHeight: 80, ObservedBlockHash: hash,
			SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
		}
		require.NoError(t, heightsync.SignAck(hosts[0], ack))
		_, _, err = sm.ApplyLocalBestEffort(2, []*types.DevshardTx{
			{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
		})
		require.NoError(t, err)

		_, applied, err := sm.ApplyLocalBestEffort(3, []*types.DevshardTx{{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq: 2, ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 3,
			}},
		}})
		require.ErrorIs(t, err, heightsync.ErrHeightRegression)
		require.Nil(t, applied)
		require.Equal(t, uint64(2), sm.LatestNonce())
	})

	t.Run("L1", func(t *testing.T) {
		sm, _ := newTestSM(t, hosts, 100000)
		_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq: 0, ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 3,
			}},
		}})
		require.ErrorIs(t, err, heightsync.ErrBadFraming)
		require.Nil(t, applied)
		require.Equal(t, uint64(0), sm.LatestNonce())
	})

	t.Run("L2", func(t *testing.T) {
		sm, _ := newTestSM(t, hosts, 100000)
		_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq: 1, ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 3,
			}},
		}})
		require.NoError(t, err)

		ack := &types.MsgHeightAck{
			TurnSeq: 1, RefNonce: 1, SlotId: 0, ObservedHeight: 50, ObservedBlockHash: hash,
			SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
		}
		require.NoError(t, heightsync.SignAck(hosts[0], ack))
		ack.HostSig[0] ^= 0xff
		_, applied, err := sm.ApplyLocalBestEffort(2, []*types.DevshardTx{
			{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
		})
		require.ErrorIs(t, err, heightsync.ErrAckSigInvalid)
		require.Nil(t, applied)
		require.Equal(t, uint64(1), sm.LatestNonce())
	})
}

func TestApplyLocalBestEffort_LogPlaneInvalidAckDroppedKeepsHeartbeat(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 100000)
	hash := []byte{0xaa}
	hb := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq: 1, ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: 3,
	}}}
	ack := &types.MsgHeightAck{
		TurnSeq: 1, RefNonce: 1, SlotId: 0, ObservedHeight: 50, ObservedBlockHash: hash,
		SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(hosts[0], ack))
	ack.HostSig[0] ^= 0xff

	_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{
		hb,
		{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
	})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.NotNil(t, applied[0].GetHeartbeat())
	require.Equal(t, uint64(1), sm.LatestNonce())
}

func TestApplyLocalBestEffort_LateAckAfterTurnPruneComposesAndApplies(t *testing.T) {
	// A late ack whose heartbeat is still in heartbeatAt composes on the
	// sequencer path and ApplyDiff-s on a host that replayed the same log.
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	newSM := func() *StateMachine {
		t.Helper()
		sm, err := NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier,
			testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000))
		require.NoError(t, err)
		return sm
	}
	composer, hostSM := newSM(), newSM()
	const n = heightsync.DefaultTurnRetain + 5
	for i := uint64(1); i <= n; i++ {
		d := testutil.SignDiff(t, user, "escrow-1", i, []*types.DevshardTx{{
			Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq: i, SlotsNum: 3, Reason: "quiet_session",
			}},
		}})
		_, err := composer.ApplyDiff(d)
		require.NoError(t, err)
		_, err = hostSM.ApplyDiff(d)
		require.NoError(t, err)
	}
	require.Nil(t, composer.HeightSyncTurnRecord(1))
	composer.mu.RLock()
	seq, ok := composer.turnTracker.HeartbeatTurn(1)
	composer.mu.RUnlock()
	require.True(t, ok)
	require.Equal(t, uint64(1), seq)

	hash := []byte{0xaa}
	ack := &types.MsgHeightAck{
		TurnSeq: 1, RefNonce: 1, SlotId: 0, ObservedHeight: 50, ObservedBlockHash: hash,
		SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(hosts[0], ack))
	ackTx := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}
	_, applied, err := composer.ApplyLocalBestEffort(n+1, []*types.DevshardTx{ackTx})
	require.NoError(t, err)
	require.Len(t, applied, 1)

	d := testutil.SignDiff(t, user, "escrow-1", n+1, applied)
	_, err = hostSM.ApplyDiff(d)
	require.NoError(t, err)
	require.Equal(t, n+1, hostSM.LatestNonce())
}

func l7HeartbeatTx(slots uint64) *types.DevshardTx {
	hash := []byte{0xaa}
	return &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq: 1, ObservedHeight: 50, ObservedBlockHash: hash, SlotsNum: slots,
		SyncVector: []*types.SyncVectorEntry{{
			SlotId: 0, Status: types.AckStatus_ACKED, ObservedHeight: 40, AckNonce: 9,
		}},
	}}}
}

func TestValidateDiff_FailedApplyTxDoesNotLeakMarks(t *testing.T) {
	// L7 marks from a log-plane-OK diff must not land if applyTx then fails.
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 100000)
	d := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{
		l7HeartbeatTx(3),
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}},
	})
	_, err := sm.ValidateDiff(d)
	require.ErrorIs(t, err, types.ErrInferenceNotFound)
	require.Empty(t, sm.HeightSyncMarks())
	require.Equal(t, uint64(0), sm.LatestNonce())
}

func TestValidateDiff_MarksFlushOnlyOnCommit(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 100000)
	d := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{l7HeartbeatTx(3)})
	vd, err := sm.ValidateDiff(d)
	require.NoError(t, err)
	require.Empty(t, sm.HeightSyncMarks(), "trial apply must not record marks")
	require.Equal(t, uint64(0), sm.LatestNonce())
	require.True(t, sm.CommitValidated(vd))
	var kinds []heightsync.MarkKind
	for _, m := range sm.HeightSyncMarks() {
		kinds = append(kinds, m.Kind)
	}
	require.Contains(t, kinds, heightsync.MarkVectorContradiction)
}
