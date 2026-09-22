package host

import (
	"context"
	"time"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/signing"
	"devshard/types"
)

// SetRepairProbe injects the unicast sender (Server.RepairProbe).
func (h *Host) SetRepairProbe(fn heightsync.RepairProbeFn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.repairProbe = fn
}

// SetCloseReadyArmed is a test seam. Production never calls it.
func (h *Host) SetCloseReadyArmed(armed bool) {
	if h.closeReady != nil {
		h.closeReady.ForceArmed(armed)
	}
}

// SetCloseReadyClock replaces the silence clock. Test seam; production uses
// time.Now.
func (h *Host) SetCloseReadyClock(now func() time.Time) {
	if h.closeReady != nil {
		h.closeReady.SetClock(now)
	}
}

// CloseReadySilentFor is how long this host has heard nothing from the user.
func (h *Host) CloseReadySilentFor() time.Duration {
	if h.closeReady == nil {
		return 0
	}
	return h.closeReady.SilentFor()
}

// CloseReadyArmed reports whether this host is armed for USER_TIMEOUT.
func (h *Host) CloseReadyArmed() bool {
	if h.closeReady == nil {
		return false
	}
	armed, _ := h.closeReady.Armed()
	return armed
}

// RepairBudget returns the probe budget (tests).
func (h *Host) RepairBudget() *heightsync.RepairBudget {
	return h.repairBudget
}

// RepairResponderBudget returns the incoming-probe budget (tests).
func (h *Host) RepairResponderBudget() *heightsync.RepairResponderBudget {
	return h.repairResponder
}

// HeightSyncTurnRecord is the SM turn copy, or nil.
func (h *Host) HeightSyncTurnRecord(turnStart uint64) *heightsync.SyncTurnRecord {
	return h.sm.HeightSyncTurnRecord(turnStart)
}

// HeightSyncMarks returns marks recorded on applyCore.
func (h *Host) HeightSyncMarks() []heightsync.AttributableMark {
	return h.sm.HeightSyncMarks()
}

// PeerSeenHas reports whether slot's repair/Diff tip is fresh.
func (h *Host) PeerSeenHas(slot uint32) bool {
	if h.peerSeen == nil {
		return false
	}
	return h.peerSeen.Has(slot, time.Now())
}

// PeerSeenHeight is the last ingested claim for slot.
func (h *Host) PeerSeenHeight(slot uint32) uint64 {
	if h.peerSeen == nil {
		return 0
	}
	return h.peerSeen.Height(slot)
}

// MaybeRepair probes missing acks past D_ack. Never attributes, never
// broadcasts, never writes a mark. Safe to call concurrently; overlapping
// runs are dropped. The local oracle tip is sent on the signed probe; it is
// not folded into the SM turn tracker.
func (h *Host) MaybeRepair(ctx context.Context) {
	if h == nil || h.repairBudget == nil {
		return
	}
	h.mu.Lock()
	probe := h.repairProbe
	h.mu.Unlock()
	if probe == nil {
		return
	}
	if !h.repairInFlight.CompareAndSwap(false, true) {
		return
	}
	defer h.repairInFlight.Store(false)

	if h.CloseReadyArmed() {
		return
	}

	hNow, hash := h.observedTip(ctx)
	if hNow == 0 {
		return
	}
	due := h.sm.HeightSyncRepairDue()
	if len(due) == 0 {
		return
	}
	h.repairBudget.Prune()

	for _, tgt := range due {
		for _, j := range tgt.Missing {
			if err := ctx.Err(); err != nil {
				return
			}
			if h.CloseReadyArmed() {
				return
			}
			delay, skip := h.repairBudget.Begin(tgt.TurnStart, j, h.CloseReadyArmed())
			if skip == heightsync.RepairSkipArmed {
				return
			}
			if skip != heightsync.RepairSkipNone {
				continue
			}
			if err := h.repairBudget.Sleep(ctx, delay); err != nil {
				return
			}
			still := h.sm.HeightSyncMissingAcks(tgt.TurnStart)
			landed := true
			for _, s := range still {
				if s == j {
					landed = false
					break
				}
			}
			if skip := h.repairBudget.AfterWait(tgt.TurnStart, j, landed); skip != heightsync.RepairSkipNone {
				continue
			}

			req := &heightsync.RepairRequest{
				TurnStart:         tgt.TurnStart,
				RefNonce:          heightsync.HeartbeatNonceForSlot(tgt.TurnStart, j, uint32(len(h.group))),
				RequesterSlot:     h.PrimarySlot(),
				ObservedHeight:    hNow,
				ObservedBlockHash: append([]byte(nil), hash...),
			}
			if err := heightsync.SignRepairRequest(h.signer, req); err != nil {
				logging.Debug("repair request sign failed", "subsystem", "heightsync",
					"escrow", h.escrowID, "slot", j, "error", err)
				h.repairBudget.Record(tgt.TurnStart, j, heightsync.RepairOutcomeUnreachable)
				continue
			}
			resp, err := probe(ctx, j, req)
			if err != nil || resp == nil || resp.Outcome != heightsync.RepairOutcomeHeight {
				h.repairBudget.Record(tgt.TurnStart, j, heightsync.RepairOutcomeUnreachable)
				continue
			}
			h.repairBudget.Record(tgt.TurnStart, j, heightsync.RepairOutcomeHeight)
			h.ingestRepairHeight(j, resp)
		}
	}
}

func (h *Host) observedTip(ctx context.Context) (uint64, []byte) {
	if h.oracle == nil {
		return 0, nil
	}
	hdr, err := h.oracle.Latest(ctx)
	if err != nil || hdr == nil || hdr.Height <= 0 {
		return 0, nil
	}
	return uint64(hdr.Height), append([]byte(nil), hdr.BlockHash...)
}

func (h *Host) ingestRepairHeight(slot uint32, resp *heightsync.RepairResponse) {
	if h.peerSeen == nil {
		h.peerSeen = heightsync.NewPeerSeen(uint32(len(h.group)), 0)
	}
	h.peerSeen.MarkFresh(slot, resp.ObservedHeight, time.Now())
	if resp.Ack == nil {
		return
	}
	key := h.slotToAddr[slot]
	v := h.verifier
	if v == nil {
		v = signing.NewSecp256k1Verifier()
	}
	if err := heightsync.VerifyAck(v, resp.Ack, key); err != nil {
		logging.Debug("repair courtesy ack dropped", "subsystem", "heightsync",
			"escrow", h.escrowID, "slot", slot, "error", err)
		return
	}
	h.mempool.Add(MempoolEntry{
		Tx:         &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: resp.Ack}},
		ProposedAt: h.sm.LatestNonce(),
	})
}

// BuildRepairHeightResponse is the responder half: signed HEIGHT + optional ack.
// Unknown turns and exhausted responder budget reject before the oracle read
// and never assign blame.
func (h *Host) BuildRepairHeightResponse(ctx context.Context, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error) {
	if req == nil || h.sm.HeightSyncTurnRecord(req.TurnStart) == nil {
		return nil, heightsync.ErrRepairUnknownTurn
	}
	if h.repairResponder != nil && !h.repairResponder.Allow(req.TurnStart, req.RequesterSlot) {
		return nil, heightsync.ErrRepairResponderBudget
	}

	hdr, hdrErr := h.latestHeader(ctx)

	h.mu.Lock()
	defer h.mu.Unlock()

	hRef := req.ObservedHeight
	if rec := h.sm.HeightSyncTurnRecord(req.TurnStart); rec != nil && rec.HReq > 0 {
		hRef = rec.HReq
	}
	st := heightsync.EvaluateSyncStateFromHeader(h.oracle, hdr, hdrErr, hRef, h.heartbeatCfg)
	var height uint64
	var hash []byte
	if st != types.SyncState_ORACLE_UNAVAILABLE && hdr != nil && hdr.Height > 0 {
		height = uint64(hdr.Height)
		hash = append([]byte(nil), hdr.BlockHash...)
	}

	resp := &heightsync.RepairResponse{
		Outcome:           heightsync.RepairOutcomeHeight,
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		SyncState:         st,
	}

	slot := h.PrimarySlot()
	if rec := h.sm.HeightSyncTurnRecord(req.TurnStart); rec != nil && h.slotIDs[slot] {
		item := heartbeatTarget{
			nonce: req.RefNonce,
			slot:  slot,
			// The turn is not carried on the ack; ref_nonce names it.
			hb: &types.MsgHeartbeat{ObservedHeight: hRef},
		}
		ack := h.buildHeightAckLocked(item, hdr, hdrErr, time.Now())
		if err := heightsync.SignAck(h.signer, ack); err == nil {
			resp.Ack = ack
		}
	}

	if err := heightsync.SignRepairResponse(h.signer, resp); err != nil {
		return nil, err
	}
	return resp, nil
}
