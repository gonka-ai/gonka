package heightsync_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

func TestHeartbeat_QuietSessionOpensTurn(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	hb := heightsync.NewHeartbeat(cfg)
	hb.SetRoster(4, 3)
	t0 := time.Unix(1_700_000_000, 0)

	// No turnover yet, so the first check is due regardless of the clock: the
	// producer cannot have agreed on a height with anyone.
	due, reason := hb.Due(t0, 500)
	require.True(t, due)
	require.Equal(t, heightsync.ReasonQuietSession, reason)
	require.Equal(t, t0.Add(cfg.Interval+cfg.TurnTimeout), hb.Deadline(t0))

	hb.OpenTurn(t0)
	due, _ = hb.Due(t0, 500)
	require.False(t, due, "an open turn suppresses a second span")

	require.False(t, hb.NoteClaim(0, t0))
	require.False(t, hb.NoteClaim(1, t0))
	require.True(t, hb.NoteClaim(2, t0), "Q host-signed claims are one full turnover")
	require.Equal(t, 1, hb.Turnovers())

	due, _ = hb.Due(t0.Add(cfg.Interval-time.Millisecond), 500)
	require.False(t, due, "inside Interval the obligation is discharged")

	due, reason = hb.Due(t0.Add(cfg.Interval), 500)
	require.True(t, due, "Interval elapsed with no turnover")
	require.Equal(t, heightsync.ReasonQuietSession, reason)
}

func TestHeartbeat_RepeatedSlotClaimsAreNotAQuorum(t *testing.T) {
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	hb.SetRoster(4, 3)
	t0 := time.Unix(1_700_000_000, 0)
	hb.OpenTurn(t0)
	for i := 0; i < 5; i++ {
		require.False(t, hb.NoteClaim(1, t0), "one chatty slot is not a turnover")
	}
	require.Zero(t, hb.Turnovers())
}

func TestHeartbeat_StalledTurnReopensAfterTurnTimeout(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	hb := heightsync.NewHeartbeat(cfg)
	hb.SetRoster(4, 3)
	t0 := time.Unix(1_700_000_000, 0)
	hb.OpenTurn(t0)
	hb.NoteClaim(0, t0) // below quorum: this turn never turns over

	due, _ := hb.Due(t0.Add(cfg.TurnTimeout-time.Millisecond), 500)
	require.False(t, due)

	due, reason := hb.Due(t0.Add(cfg.TurnTimeout), 500)
	require.True(t, due, "one unreachable slot must not silence the cadence")
	require.Equal(t, heightsync.ReasonTurnTimeout, reason)

	hb.OpenTurn(t0.Add(cfg.TurnTimeout))
	require.Equal(t, 1, hb.AbandonedTurns())
	require.Zero(t, hb.Turnovers())
}

func TestHeartbeat_SettledTurnDoesNotWaitOutTurnTimeout(t *testing.T) {
	// A degraded record leaves nothing to wait for, so the producer may retry
	// immediately instead of burning the rest of TurnTimeout.
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	hb.SetRoster(4, 3)
	t0 := time.Unix(1_700_000_000, 0)
	hb.OpenTurn(t0)
	due, _ := hb.Due(t0, 500)
	require.False(t, due)

	hb.SettleTurn()
	due, reason := hb.Due(t0, 500)
	require.True(t, due)
	require.Equal(t, heightsync.ReasonQuietSession, reason)
	require.Zero(t, hb.Turnovers(), "settling a degraded turn is not a turnover")
}

func TestHeartbeat_NoObservedHeightSkips(t *testing.T) {
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	due, reason := hb.Due(time.Unix(1_700_000_000, 0), 0)
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
	// The record is still evaluated on logged heights: request span and acks at
	// the same height complete the turn, whatever the producer's clock did.
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
	require.True(t, tr.CompletedAtOrAbove(500))
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

func TestTurnTracker_QuorumCompletesTurn(t *testing.T) {
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
	require.True(t, tr.CompletedAtOrAbove(500), "bookkeeping only: (C-turn) is withdrawn")
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
	require.False(t, tr.CompletedAtOrAbove(500))
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
	require.True(t, tr.CompletedAtOrAbove(500))
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
	require.False(t, tr.CompletedAtOrAbove(500))
}

// TestHeightAck_OracleUnavailableCountsTowardQuorum pins the liveness half of
// withdrawing (C-turn): a turn now certifies reachability, so a host with a dead
// follower holds up its end of the cadence. It echoes the floor from the log it
// already applies (here 500, the height its own heartbeat carried) and labels
// itself honestly. Under (C-turn) this slot was a permanent hole.
func TestHeightAck_OracleUnavailableCountsTowardQuorum(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 500, types.SyncState_ORACLE_UNAVAILABLE),
		ackTx(1, 10, 2, 500, types.SyncState_SYNCED),
	}, 500)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, rec.Acks[1].SyncState,
		"the record keeps the self-report: the slot is transparently no height witness")
}

func TestTurnTracker_InferenceStampAdvancesHLast(t *testing.T) {
	tr := heightsync.NewTurnTracker(3, 0, heightsync.DefaultHeartbeatConfig())
	hash := []byte{0xaa}
	tr.Observe(1, []*types.DevshardTx{{
		Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: 1, ObservedHeight: 100, ObservedBlockHash: hash,
		}},
	}}, 100)
	require.Equal(t, uint64(100), tr.LastCompletedHeight())
}

func TestHeartbeat_ExecutorClaimsDischargeCadence(t *testing.T) {
	// A stamped executor response proves the same round-trip a heartbeat ack
	// does, which is why a busy session owes no heartbeat. The producer's own
	// stamp is not a claim: it proves nothing about what any host saw.
	cfg := heightsync.DefaultHeartbeatConfig()
	hb := heightsync.NewHeartbeat(cfg)
	hb.SetRoster(3, 2)
	t0 := time.Unix(1_700_000_000, 0)

	require.False(t, hb.NoteClaim(0, t0))
	require.True(t, hb.NoteClaim(1, t0))

	due, _ := hb.Due(t0.Add(cfg.Interval/2), 100)
	require.False(t, due)
	require.Equal(t, cfg.Interval/2, hb.SinceTurnover(t0.Add(cfg.Interval/2)))
}
