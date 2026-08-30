package state

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

func TestHeightSync_SnapshotRestoreAgreesOnRootAndFloor(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000)

	newSM := func() *StateMachine {
		t.Helper()
		sm, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(),
			signing.NewSecp256k1Verifier(), store)
		require.NoError(t, err)
		return sm
	}

	live := newSM()
	hash := []byte{0xaa}
	apply := func(sm *StateMachine, persist bool, nonce uint64, txs ...*types.DevshardTx) []byte {
		t.Helper()
		d := testutil.SignDiff(t, user, "escrow-1", nonce, txs)
		root, err := sm.ApplyDiff(d)
		require.NoError(t, err)
		if persist {
			require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: d, StateHash: root}))
		}
		return root
	}

	apply(live, true, 1,
		&types.DevshardTx{Tx: &types.DevshardTx_ForceHeightSyncTurn{ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{
			TriggerNonce: 1, EndNonce: 3, SlotsNum: 3, AnchorK: 10, Reason: "heartbeat",
		}}},
		snapHeartbeat(1, 50, 3, hash),
	)
	apply(live, true, 2, snapAck(t, hosts[0], 1, 1, 0, 50, hash))
	apply(live, true, 3, snapAck(t, hosts[1], 1, 1, 1, 50, hash))

	st := live.ExportState()
	require.Equal(t, uint64(1), st.HeightSyncForcedStart)
	require.Equal(t, uint64(3), st.HeightSyncForcedEnd)
	require.Equal(t, uint64(10), st.HeightSyncTurnK)

	data, err := types.MarshalStateSnapshotProto(st, nil, nil)
	require.NoError(t, err)
	restoredState, _, _, err := types.UnmarshalStateSnapshotProto(data)
	require.NoError(t, err)
	require.Equal(t, st.HeightSyncForcedStart, restoredState.HeightSyncForcedStart)
	require.Equal(t, st.HeightSyncTurnReason, restoredState.HeightSyncTurnReason)

	restored := newSM()
	restored.RestoreState(restoredState)

	got := restored.ExportState()
	require.Equal(t, st.HeightSyncForcedStart, got.HeightSyncForcedStart)
	require.Equal(t, st.HeightSyncForcedEnd, got.HeightSyncForcedEnd)
	require.Equal(t, st.HeightSyncCadenceSwallowUntil, got.HeightSyncCadenceSwallowUntil)
	require.Equal(t, st.HeightSyncSwallowFe, got.HeightSyncSwallowFe)
	require.Equal(t, st.HeightSyncTurnK, got.HeightSyncTurnK)
	require.Equal(t, st.HeightSyncTurnSlots, got.HeightSyncTurnSlots)
	require.Equal(t, st.HeightSyncTurnReason, got.HeightSyncTurnReason)

	for _, m := range []uint64{2, 3, 4} {
		wantH, wantHash, wantKnown := live.HeightSyncFloorAsOf(m)
		gotH, gotHash, gotKnown := restored.HeightSyncFloorAsOf(m)
		require.Equal(t, wantKnown, gotKnown, "AsOf(%d)", m)
		require.Equal(t, wantH, gotH, "AsOf(%d)", m)
		require.Equal(t, wantHash, gotHash, "AsOf(%d)", m)
	}

	liveRec := live.HeightSyncTurnRecord(1)
	restRec := restored.HeightSyncTurnRecord(1)
	require.NotNil(t, liveRec)
	require.Equal(t, liveRec, restRec)
	require.Equal(t, heightsync.TurnComplete, restRec.State)

	apply(live, false, 4, snapHeartbeat(2, 60, 3, hash))
	apply(restored, false, 4, snapHeartbeat(2, 60, 3, hash))

	liveRoot, err := live.ComputeStateRoot()
	require.NoError(t, err)
	restRoot, err := restored.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, liveRoot, restRoot)

	bad := testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{snapHeartbeat(3, 10, 3, hash)})
	_, err = live.ApplyDiff(bad)
	require.ErrorIs(t, err, heightsync.ErrHeightRegression)
	_, err = restored.ApplyDiff(bad)
	require.ErrorIs(t, err, heightsync.ErrHeightRegression)
}

type failGetDiffsStore struct {
	*storage.Memory
}

func (s *failGetDiffsStore) GetDiffs(string, uint64, uint64) ([]types.DiffRecord, error) {
	return nil, errors.New("getdiffs failed")
}

func TestHeightSync_RestoreGetDiffsErrorKeepsLastCompletedHeight(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000)

	live, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(),
		signing.NewSecp256k1Verifier(), store)
	require.NoError(t, err)
	hash := []byte{0xaa}
	apply := func(nonce uint64, txs ...*types.DevshardTx) {
		t.Helper()
		d := testutil.SignDiff(t, user, "escrow-1", nonce, txs)
		root, err := live.ApplyDiff(d)
		require.NoError(t, err)
		require.NoError(t, store.AppendDiff("escrow-1", types.DiffRecord{Diff: d, StateHash: root}))
	}
	apply(1, snapHeartbeat(1, 50, 3, hash))
	apply(2, snapAck(t, hosts[0], 1, 1, 0, 50, hash))
	apply(3, snapAck(t, hosts[1], 1, 1, 1, 50, hash))

	st := live.ExportState()
	require.Equal(t, uint64(50), st.HeightSyncLastCompletedHeight)
	require.Greater(t, st.LatestNonce, uint64(0))

	failStore := &failGetDiffsStore{Memory: testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000)}
	restored, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(),
		signing.NewSecp256k1Verifier(), failStore)
	require.NoError(t, err)
	restored.RestoreState(st)

	got := restored.ExportState()
	require.Equal(t, st.HeightSyncLastCompletedHeight, got.HeightSyncLastCompletedHeight)
	require.Equal(t, st.HeightSyncLatestTurnSeq, got.HeightSyncLatestTurnSeq)
	require.Equal(t, st.HeightSyncLastCompletedHeight, restored.HeightSyncCloneTurnTracker().LastCompletedHeight())
}

func snapHeartbeat(turn, height, slots uint64, hash []byte) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq:           turn,
		ObservedHeight:    height,
		ObservedBlockHash: append([]byte(nil), hash...),
		SlotsNum:          slots,
		Reason:            string(heightsync.ReasonQuietSession),
	}}}
}

func snapAck(t *testing.T, signer *signing.Secp256k1Signer, turn, ref uint64, slot uint32, height uint64, hash []byte) *types.DevshardTx {
	t.Helper()
	ack := &types.MsgHeightAck{
		TurnSeq:           turn,
		RefNonce:          ref,
		SlotId:            slot,
		ObservedHeight:    height,
		ObservedBlockHash: append([]byte(nil), hash...),
		SyncState:         types.SyncState_SYNCED,
		PeerSeen:          []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(signer, ack))
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}
}
