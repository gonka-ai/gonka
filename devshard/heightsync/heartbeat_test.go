package heightsync_test

import (
	"math"
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
		TurnSeq:           turn,
		ObservedHeight:    height,
		ObservedBlockHash: []byte{0xaa},
		SlotsNum:          slots,
		Reason:            string(heightsync.ReasonQuietSession),
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

// pastAckWindow is the first height at which the shipped ack window has closed
// on a turn requested at h_req.
func pastAckWindow(hReq uint64) uint64 {
	return hReq + heightsync.DefaultHeartbeatConfig().AckDeadlineBlocks + 1
}

func TestTurnTracker_BelowQuorumDegradesNoBlame(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 500, types.SyncState_SYNCED),
	}, 500)
	tr.AdvanceHeight(pastAckWindow(500))
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnDegraded, rec.State)
	require.Zero(t, tr.LastCompletedHeight())
	require.False(t, tr.CompletedAtOrAbove(500))
	require.Equal(t, []uint32{2, 3}, tr.MissingAcks(1))
}

func TestLateAck_DoesNotClearDegraded(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	past := pastAckWindow(500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
	}, past)
	require.Equal(t, heightsync.TurnDegraded, tr.Record(1).State)

	tr.Observe(20, []*types.DevshardTx{
		ackTx(1, 10, 1, past+1, types.SyncState_SYNCED),
		ackTx(1, 10, 2, past+1, types.SyncState_SYNCED),
	}, past+1)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnDegraded, rec.State, "late acks never un-degrade")
	require.True(t, rec.Acks[1].Late)
	require.True(t, rec.Acks[2].Late)
	require.Equal(t, past+1, rec.Acks[2].Height, "late ack still admitted for height")
	require.Zero(t, tr.LastCompletedHeight())
}

func TestTurnTracker_IngestNextBlockSameStampCompletes(t *testing.T) {
	// Ingest height may tick during transport; stamps still at h_req count.
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

// TestTurnTracker_SpanAcrossBlockBoundariesCompletes is step 4's headline case.
// A four-slot span is dispatched slot by slot, so the acks answering it are
// stamped at climbing heights while h_req stays at the height the whole span was
// composed at. The window now covers the producer's own turnover budget, so the
// turn completes and nothing is owed a repair probe. Against the old one-block
// window every ack past the first block was late and the turn degraded in steady
// state — asserted below, so the regression cannot come back quietly.
func TestTurnTracker_SpanAcrossBlockBoundariesCompletes(t *testing.T) {
	span := func(cfg heightsync.HeartbeatConfig) *heightsync.TurnTracker {
		tr := heightsync.NewTurnTracker(4, 3, cfg)
		for i, nonce := range heightsync.SpanNonces(10, 4) {
			// The span carries one height; the chain ticks as it is dispatched.
			tr.Observe(nonce, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500+uint64(i))
		}
		tr.Observe(14, []*types.DevshardTx{
			ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
			ackTx(1, 11, 1, 501, types.SyncState_SYNCED),
			ackTx(1, 12, 2, 502, types.SyncState_SYNCED),
			ackTx(1, 13, 3, 503, types.SyncState_SYNCED),
		}, 503)
		return tr
	}

	tr := span(heightsync.DefaultHeartbeatConfig())
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnComplete, rec.State)
	require.Equal(t, uint64(500), rec.HReq)
	for slot, ack := range rec.Acks {
		require.False(t, ack.Late, "slot %d answered inside the turn's own budget", slot)
	}
	require.Empty(t, tr.MissingAcksDue(1, 503), "no probe is due while the window is open")
	require.Equal(t, uint64(503), tr.LastCompletedHeight())

	narrow := heightsync.DefaultHeartbeatConfig()
	narrow.AckDeadlineBlocks = 1
	old := span(narrow).Record(1)
	require.Equal(t, heightsync.TurnDegraded, old.State, "the pre-step-4 window")
	require.True(t, old.Acks[2].Late)
	require.True(t, old.Acks[3].Late)
}

func TestTurnTracker_StampPastDeadlineDegrades(t *testing.T) {
	// observed_height > h_req + D_ack is late even if ingest is still inside the
	// window: the ack's own stamp is the host's timestamp on its answer.
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	past := pastAckWindow(500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, past, types.SyncState_SYNCED),
		ackTx(1, 10, 1, past, types.SyncState_SYNCED),
		ackTx(1, 10, 2, past, types.SyncState_SYNCED),
	}, 501)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnDegraded, rec.State)
	require.True(t, rec.Acks[0].Late)
	require.Zero(t, tr.LastCompletedHeight())
	require.False(t, tr.CompletedAtOrAbove(500))
}

func TestTurnTracker_AckDeadlineDoesNotWrap(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	cfg.AckDeadlineBlocks = 10
	tr := heightsync.NewTurnTracker(3, 0, cfg)
	hReq := uint64(math.MaxUint64 - 1)
	tr.Observe(1, []*types.DevshardTx{heartbeatTx(1, hReq, 3)}, hReq)
	ackH := hReq + 1
	tr.Observe(2, []*types.DevshardTx{
		ackTx(1, 1, 0, ackH, types.SyncState_SYNCED),
		ackTx(1, 1, 1, ackH, types.SyncState_SYNCED),
	}, ackH)
	rec := tr.Record(1)
	require.NotNil(t, rec)
	require.False(t, rec.Acks[0].Late, "honest ack at HReq+1 must not be late when HReq+D_ack would wrap")
	require.False(t, rec.Acks[1].Late)
	require.Equal(t, heightsync.TurnComplete, rec.State)
}

// TestTurnTracker_CompletedTurnIsFinal closes the mirror of attack 22: a slot
// that answered in time re-acks at a height past the deadline, which would
// otherwise drag its own record late and pull the count back under quorum.
func TestTurnTracker_CompletedTurnIsFinal(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 500, types.SyncState_SYNCED),
		ackTx(1, 10, 2, 500, types.SyncState_SYNCED),
	}, 500)
	require.Equal(t, heightsync.TurnComplete, tr.Record(1).State)

	past := pastAckWindow(500)
	tr.Observe(20, []*types.DevshardTx{ackTx(1, 10, 0, past, types.SyncState_SYNCED)}, past)
	rec := tr.Record(1)
	require.Equal(t, heightsync.TurnComplete, rec.State, "a settled turn is history")
	require.Equal(t, uint64(500), rec.CompletedAtHeight,
		"h_last records where the turn closed, not how far the log has since run")
}

// TestTurnTracker_HashlessHeartbeatDoesNotPinTheWindow keeps the spec §14
// presence rule out of the turn window: a height with no hash is not a stamp,
// so it cannot pin
// h_req low and make every honest ack late.
func TestTurnTracker_HashlessHeartbeatDoesNotPinTheWindow(t *testing.T) {
	hashless := heartbeatTx(1, 400, 4)
	hashless.GetHeartbeat().ObservedBlockHash = nil

	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(1, 500, 4)}, 500)
	tr.Observe(11, []*types.DevshardTx{hashless}, 500)
	require.Equal(t, uint64(500), tr.Record(1).HReq)

	tr.Observe(14, []*types.DevshardTx{
		ackTx(1, 10, 0, 501, types.SyncState_SYNCED),
		ackTx(1, 10, 1, 501, types.SyncState_SYNCED),
		ackTx(1, 10, 2, 501, types.SyncState_SYNCED),
	}, 501)
	require.Equal(t, heightsync.TurnComplete, tr.Record(1).State)
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

func TestTurnTracker_PrunesCompletedTurns(t *testing.T) {
	const slots uint64 = 3
	tr := heightsync.NewTurnTracker(slots, 2, heightsync.DefaultHeartbeatConfig())
	const n = 200
	hash := []byte{0xaa}
	_ = hash
	for i := uint64(1); i <= n; i++ {
		h := 100 + i
		nonce := i
		txs := []*types.DevshardTx{
			heartbeatTx(i, h, slots),
			ackTx(i, nonce, 0, h, types.SyncState_SYNCED),
			ackTx(i, nonce, 1, h, types.SyncState_SYNCED),
		}
		tr.Observe(nonce, txs, h)
	}
	require.LessOrEqual(t, tr.TurnCount(), int(heightsync.DefaultTurnRetain)+1,
		"completed turns outside the retain window must be evicted")
	require.Equal(t, int(n), tr.HeartbeatAtCount(), "heartbeatAt is not pruned with the turn record")
	require.Equal(t, uint64(n), tr.MaxTurnSeq())
	require.Equal(t, uint64(100+n), tr.LastCompletedHeight(), "h_last survives prune")

	_, ok := tr.HeartbeatTurn(n)
	require.True(t, ok, "L3 still finds a heartbeat inside the retain window")
	_, ok = tr.HeartbeatTurn(1)
	require.True(t, ok, "L3 still finds a heartbeat after its turn record is pruned")
	require.NotNil(t, tr.Record(n-1), "L7 still sees turn_seq-1")
	require.Nil(t, tr.Record(1))

	latest := tr.Latest()
	require.NotNil(t, latest)
	require.Equal(t, heightsync.TurnComplete, latest.State)
	require.Empty(t, tr.MissingAcksDue(n, 100+n), "complete turn: probe not due")
}

func TestTurnTracker_PrunesOpenTurns(t *testing.T) {
	const n = 5000
	bound := int(heightsync.DefaultTurnRetain) + 1

	t.Run("unstamped", func(t *testing.T) {
		tr := heightsync.NewTurnTracker(1, 1, heightsync.DefaultHeartbeatConfig())
		for i := uint64(1); i <= n; i++ {
			tx := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
				TurnSeq:  i,
				SlotsNum: 1,
			}}}
			tr.Observe(i, []*types.DevshardTx{tx}, 0)
		}
		require.LessOrEqual(t, tr.TurnCount(), bound)
		require.Equal(t, n, tr.HeartbeatAtCount())
		require.Equal(t, uint64(n), tr.MaxTurnSeq())
		require.Nil(t, tr.Record(1))
		require.NotNil(t, tr.Latest())
		require.Equal(t, heightsync.TurnOpen, tr.Latest().State)
		_, ok := tr.HeartbeatTurn(1)
		require.True(t, ok)
	})

	t.Run("flat_stamped", func(t *testing.T) {
		tr := heightsync.NewTurnTracker(1, 1, heightsync.DefaultHeartbeatConfig())
		const h uint64 = 100
		for i := uint64(1); i <= n; i++ {
			tr.Observe(i, []*types.DevshardTx{heartbeatTx(i, h, 1)}, h)
		}
		require.LessOrEqual(t, tr.TurnCount(), bound)
		require.Equal(t, n, tr.HeartbeatAtCount())
		require.Nil(t, tr.Record(1))
		latest := tr.Latest()
		require.NotNil(t, latest)
		require.Equal(t, heightsync.TurnOpen, latest.State)
		require.Equal(t, h, latest.HReq)
	})
}
