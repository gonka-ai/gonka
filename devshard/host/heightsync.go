package host

import (
	"context"
	"time"

	"devshard/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/logging"
	"devshard/types"
)

type heartbeatTarget struct {
	nonce uint64
	slot  uint32
	hb    *types.MsgHeartbeat
}

// maybeAckHeartbeatsLocked emits one MsgHeightAck per newly applied heartbeat
// addressed to a slot this host owns. Caller holds h.mu.
//
// The oracle is read once for the whole request so ack stamps match the
// response-leg Anchor of this exchange (honest L4). An ack is required even
// when Latest() fails (ORACLE_UNAVAILABLE); silence is worse for the roster.
func (h *Host) maybeAckHeartbeatsLocked(diffs []types.Diff, hdr *blocks.Header, hdrErr error) {
	if len(diffs) == 0 {
		return
	}
	if h.peerSeen == nil {
		h.peerSeen = heightsync.NewPeerSeen(uint32(len(h.group)), 0)
	}

	slotsNum := uint64(len(h.group))
	now := time.Now()
	var mine []heartbeatTarget
	for _, diff := range diffs {
		for _, tx := range diff.Txs {
			if tx == nil {
				continue
			}
			if hb := tx.GetHeartbeat(); hb != nil {
				slot := heightsync.SlotForNonce(diff.Nonce, slotsNum)
				h.peerSeen.MarkFresh(slot, hb.ObservedHeight, now)
				if h.slotIDs[slot] {
					mine = append(mine, heartbeatTarget{nonce: diff.Nonce, slot: slot, hb: hb})
				}
			}
			if ack := tx.GetHeightAck(); ack != nil {
				h.peerSeen.MarkFresh(ack.SlotId, ack.ObservedHeight, now)
			}
		}
	}
	if len(mine) == 0 {
		return
	}

	for _, item := range mine {
		ack := h.buildHeightAckLocked(item, hdr, hdrErr, now)
		if err := heightsync.SignAck(h.signer, ack); err != nil {
			logging.Warn("height ack sign failed", "subsystem", "heightsync",
				"escrow", h.escrowID, "nonce", item.nonce, "slot", item.slot, "error", err)
			continue
		}
		h.mempool.Add(MempoolEntry{
			Tx:         &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
			ProposedAt: h.sm.LatestNonce(),
		})
	}
}

func (h *Host) latestHeader(ctx context.Context) (*blocks.Header, error) {
	if h.oracle == nil {
		return nil, ErrNoChainOracle
	}
	return h.oracle.Latest(ctx)
}

func (h *Host) latestHeaderIf(ctx context.Context, need bool) (*blocks.Header, error) {
	if !need {
		return nil, nil
	}
	return h.latestHeader(ctx)
}

func headerStamp(hdr *blocks.Header, err error) (uint64, []byte) {
	if err != nil || hdr == nil || hdr.Height <= 0 || !heightsync.StampPresent(hdr.BlockHash) {
		return 0, nil
	}
	return uint64(hdr.Height), append([]byte(nil), hdr.BlockHash...)
}

func (h *Host) oracleNeededLocked(req HostRequest, newlyApplied []types.Diff) bool {
	if h.hasOwnHeartbeatLocked(newlyApplied) {
		return true
	}
	if h.closeReady != nil && len(newlyApplied) > 0 {
		var claim uint64
		for _, d := range newlyApplied {
			for _, tx := range d.Txs {
				if th := contactHeight(tx); th > claim {
					claim = th
				}
			}
		}
		if claim == 0 {
			return true
		}
	}
	return h.receiptWantsOracleLocked(req)
}

func (h *Host) hasOwnHeartbeatLocked(diffs []types.Diff) bool {
	slotsNum := uint64(len(h.group))
	for _, diff := range diffs {
		for _, tx := range diff.Txs {
			if tx == nil {
				continue
			}
			if hb := tx.GetHeartbeat(); hb != nil {
				slot := heightsync.SlotForNonce(diff.Nonce, slotsNum)
				if h.slotIDs[slot] {
					return true
				}
			}
		}
	}
	return false
}

func (h *Host) receiptWantsOracleLocked(req HostRequest) bool {
	if req.Payload == nil || h.findDiff(req.Diffs, req.Nonce) == nil || len(h.group) == 0 {
		return false
	}
	executorSlot := h.group[req.Nonce%uint64(len(h.group))].SlotID
	return h.slotIDs[executorSlot]
}

// buildHeightAckLocked produces the ack for one heartbeat addressed to this host.
//
// The height it puts in Diff is a *reference* height — max(own tip, floor) —
// exactly like an executor receipt, because the log has one height semantics
// (spec §14). The host's own oracle reading stays first-party in two places that
// do not enter the log: the response-leg Anchor of this exchange, which L4 binds
// to the ack through that same max(), and sync_state, which labels divergence
// for the gateway's monitoring surface.
//
// The floor is read at ref_nonce+1, matching RefProducingNonce: the host has
// applied through ref_nonce inclusive, so the soliciting heartbeat's own stamp
// is part of the bar it must clear. Reading F(ref_nonce) instead — excluding
// that heartbeat, since AsOf is exclusive — leaves a lagging host stamping below
// the floor the verifier will judge it against, which is L0-invalid.
func (h *Host) buildHeightAckLocked(item heartbeatTarget, hdr *blocks.Header, hdrErr error, now time.Time) *types.MsgHeightAck {
	st := heightsync.EvaluateSyncStateFromHeader(h.oracle, hdr, hdrErr, item.hb.ObservedHeight, h.heartbeatCfg)
	var height uint64
	var hash []byte
	if st != types.SyncState_ORACLE_UNAVAILABLE && hdr != nil {
		if hdr.Height > 0 {
			height = uint64(hdr.Height)
		}
		hash = append([]byte(nil), hdr.BlockHash...)
		h.peerSeen.MarkFresh(item.slot, height, now)
	}
	refH, refHash := h.referenceStamp(item.nonce+1, height, hash)
	return &types.MsgHeightAck{
		TurnSeq:           item.hb.TurnSeq,
		RefNonce:          item.nonce,
		SlotId:            item.slot,
		ObservedHeight:    refH,
		ObservedBlockHash: refHash,
		SyncState:         st,
		PeerSeen:          h.peerSeen.Bytes(),
	}
}

// referenceStamp is the producer side of L0: a reference height must be
// max(own_tip, F(m)) for producing nonce m, or absent. It is never below F(m).
//
// Lifting to the floor is not a false claim. The floor is a height already in
// the log, so a stamp equal to it is self-evidently a carry: the verifier can
// see which earlier tx established that floor and who signed it, so blame for a
// bad height stays with its originator rather than with the carrier (L6). The
// host's own view stays first-party in the response-leg Anchor and in sync_state.
//
// Carrying stops where plausibility does. A floor more than W_conf above this
// host's own tip is either poisoned or on a branch this host will never see, and
// omission is legal at any floor, so the honest answer is to say nothing: an
// omitted stamp costs the roster one height claim, while a carried one puts a
// pair no chain can reconcile under another signature.
func (h *Host) referenceStamp(producingNonce, height uint64, hash []byte) (uint64, []byte) {
	floor, floorHash, known := h.sm.HeightSyncFloorAsOf(producingNonce)
	if !known || floor <= height || !heightsync.StampPresent(floorHash) {
		return height, hash
	}
	if h.heartbeatCfg.FloorOutOfReach(floor, height) {
		logging.Warn("height stamp omitted: floor out of reach", "subsystem", "heightsync",
			"escrow", h.escrowID, "nonce", producingNonce, "floor", floor, "own_tip", height)
		return 0, nil
	}
	return floor, floorHash
}
