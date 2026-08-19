package heightsync_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

func TestHeartbeat_QuietSessionOpensTurn(t *testing.T) {
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	const hLast uint64 = 100
	deadline := hb.Deadline(hLast)
	require.Equal(t, uint64(102), deadline) // 100 + K_hb=1 + D_ack=1

	due, _ := hb.Due(hLast, hLast)
	require.False(t, due, "same height as last complete turn is not due")

	due, reason := hb.Due(hLast+1, hLast)
	require.True(t, due, "K_hb=1 ⇒ due on the next block")
	require.Equal(t, heightsync.ReasonQuietSession, reason)
	require.LessOrEqual(t, hLast+1, deadline, "turn opens by h_last+K_hb+D_ack")
}

func TestHeartbeat_NoObservedHeightSkips(t *testing.T) {
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	due, reason := hb.Due(0, 10)
	require.False(t, due)
	require.Equal(t, heightsync.ReasonNoHeight, reason)
	require.Equal(t, 1, hb.SkippedNoHeight())

	txs := hb.SpanTxs(1, 0, []byte{1}, 4, heightsync.ReasonQuietSession, nil)
	require.Nil(t, txs)
	require.Equal(t, 2, hb.SkippedNoHeight())
}

func TestHeartbeat_SpanDispatchAddressesEverySlot(t *testing.T) {
	const slots uint64 = 4
	const start uint64 = 10
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	txs := hb.SpanTxs(3, 500, []byte{0xaa}, slots, heightsync.ReasonQuietSession, nil)
	require.Len(t, txs, int(slots))
	nonces := heightsync.SpanNonces(start, slots)
	require.Equal(t, []uint64{10, 11, 12, 13}, nonces)

	seen := map[uint32]int{}
	for i, tx := range txs {
		inner := tx.GetHeartbeat()
		require.NotNil(t, inner)
		require.Equal(t, uint64(3), inner.TurnSeq)
		require.Equal(t, uint64(500), inner.ObservedHeight)
		require.Equal(t, slots, inner.SlotsNum)
		seen[heightsync.SlotForNonce(nonces[i], slots)]++
	}
	require.Len(t, seen, int(slots), "every slot addressed once; no ack awaited")
	for slot, n := range seen {
		require.Equal(t, 1, n, "slot %d", slot)
	}
}

func heartbeatTx(turn, height, slots uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq:        turn,
		ObservedHeight: height,
		SlotsNum:       slots,
		Reason:         string(heightsync.ReasonQuietSession),
	}}}
}

func ackTx(turn, ref uint64, slot uint32, height uint64, st types.SyncState) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
		TurnSeq:        turn,
		RefNonce:       ref,
		SlotId:         slot,
		ObservedHeight: height,
		SyncState:      st,
	}}}
}

func TestHeartbeat_SameBlockRequestAndAckCompletes(t *testing.T) {
	// One cycle = two nonce rounds at the same height: heartbeat span, then acks.
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 2, 500, types.SyncState_SYNCED),
	}, 500)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	require.Equal(t, uint64(500), rec.CompletedAtHeight)
	require.False(t, rec.Acks[0].Late)
	require.True(t, tr.Confirms(500))
}

func TestTurnTracker_OutOfOrderAcksIdenticalRecord(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	mk := func(order []uint32) *heightsync.SyncTurnRecord {
		tr := heightsync.NewTurnTracker(4, 3, cfg)
		tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
		var txs []*types.DevshardTx
		for _, slot := range order {
			txs = append(txs, ackTx(1, 10, slot, 500, types.SyncState_SYNCED))
		}
		tr.Observe(14, txs, 500)
		return tr.Record(1)
	}
	a := mk([]uint32{2, 0, 3})
	b := mk([]uint32{3, 2, 0})
	require.Equal(t, a, b)
	require.Len(t, a.Acks, 3)
}

func TestTurnTracker_QuorumCompletesAndConfirms(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	require.Equal(t, 3, tr.Quorum(), "Q is the same knob as (C-quorum)")
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	require.Equal(t, heightsync.TurnOpen, tr.Record(1).State)

	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 2, 500, types.SyncState_ORACLE_STALE),
	}, 500)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	require.Equal(t, uint64(500), rec.CompletedAtHeight)
	require.Equal(t, uint64(500), tr.LastCompletedHeight())
	require.True(t, tr.Confirms(500), "(C-turn) confirms")
}

func TestTurnTracker_BelowQuorumDegradesNoBlame(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 500, types.SyncState_SYNCED),
	}, 500)
	tr.AdvanceHeight(502) // D_ack=1 → window closes at h_req+1
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnDegraded, rec.State)
	require.Zero(t, tr.LastCompletedHeight())
	require.False(t, tr.Confirms(500))
	require.Equal(t, []uint32{2, 3}, tr.MissingAcks(1))
}

func TestLateAck_DoesNotClearDegraded(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
	}, 503)
	require.Equal(t, heightsync.TurnDegraded, tr.Record(1).State)

	tr.Observe(20, []*types.DevshardTx{
		ackTx(1, 10, 1, 504, types.SyncState_SYNCED),
		ackTx(1, 10, 2, 504, types.SyncState_SYNCED),
	}, 504)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnDegraded, rec.State, "late acks never un-degrade")
	require.True(t, rec.Acks[1].Late)
	require.True(t, rec.Acks[2].Late)
	require.Equal(t, uint64(504), rec.Acks[2].Height, "late ack still admitted for height")
	require.Zero(t, tr.LastCompletedHeight())
}

func TestTurnTracker_IngestNextBlockSameStampCompletes(t *testing.T) {
	// D_ack=1: ingest height may tick during transport; stamps still at h_req count.
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 2, 500, types.SyncState_SYNCED),
	}, 501)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	require.False(t, rec.Acks[0].Late)
	require.True(t, tr.Confirms(500))
}

func TestTurnTracker_StampPastDeadlineDegrades(t *testing.T) {
	// D_ack=1: observed_height > h_req+D_ack is late even if ingest is still inside the window.
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 502, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 502, types.SyncState_SYNCED),
		ackTx(1, 10, 2, 502, types.SyncState_SYNCED),
	}, 501)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnDegraded, rec.State)
	require.True(t, rec.Acks[0].Late)
	require.Zero(t, tr.LastCompletedHeight())
	require.False(t, tr.Confirms(500))
}

func TestHeightAck_OracleUnavailableStillRequired(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 0, types.SyncState_ORACLE_UNAVAILABLE),
		ackTx(1, 10, 2, 500, types.SyncState_SYNCED),
	}, 500)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnOpen, rec.State, "ORACLE_UNAVAILABLE does not count toward Q")
	require.Contains(t, rec.Acks, uint32(1), "ack is still required and recorded")
	require.False(t, tr.Confirms(500), "(C-turn) unaffected")

	tr.Observe(15, []*types.DevshardTx{
		ackTx(1, 10, 3, 500, types.SyncState_SYNCED),
	}, 500)
	require.Equal(t, heightsync.TurnComplete, tr.Record(1).State)
	require.True(t, tr.Confirms(500))
}
