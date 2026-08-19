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
func (h *Host) maybeAckHeartbeatsLocked(ctx context.Context, diffs []types.Diff) {
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

	hdr, hdrErr := h.latestHeaderLocked(ctx)
	for _, item := range mine {
		ack := h.buildHeightAckLocked(item, hdr, hdrErr, now)
		if err := heightsync.SignAck(h.signer, ack); err != nil {
			logging.Warn("height ack sign failed", "subsystem", "heightsync",
				"escrow", h.escrowID, "nonce", item.nonce, "slot", item.slot, "error", err)
			continue
		}
		h.mempool.Add(MempoolEntry{
			Tx: &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}},
			ProposedAt: h.sm.LatestNonce(),
		})
	}
}

func (h *Host) latestHeaderLocked(ctx context.Context) (*blocks.Header, error) {
	if h.oracle == nil {
		return nil, ErrNoChainOracle
	}
	return h.oracle.Latest(ctx)
}

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
	return &types.MsgHeightAck{
		TurnSeq:           item.hb.TurnSeq,
		RefNonce:          item.nonce,
		SlotId:            item.slot,
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		SyncState:         st,
		PeerSeen:          h.peerSeen.Bytes(),
	}
}
