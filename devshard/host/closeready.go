package host

import (
	"context"

	"devshard/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/types"
)

func (h *Host) noteCloseReadyLocked(newlyApplied []types.Diff, hdr *blocks.Header, hdrErr error) {
	if h.closeReady == nil {
		return
	}
	last, degraded := h.sm.HeightSyncArmingContext()
	h.closeReady.SetTurnContext(last, degraded)
	if len(newlyApplied) == 0 {
		return
	}
	var claim uint64
	for _, d := range newlyApplied {
		for _, tx := range d.Txs {
			if th := contactHeight(tx); th > claim {
				claim = th
			}
		}
	}
	hNow := claim
	if hNow == 0 {
		hNow, _ = headerStamp(hdr, hdrErr)
	}
	h.closeReady.NoteContact(hNow, claim)
	h.closeReady.Evaluate(hNow)
}

// EvaluateCloseReady refreshes turn context and the height attached to arming
// evidence. Arming itself keys on elapsed silence and is also re-checked
// whenever CloseReadyView().Armed() is read, so no tick is required for
// correctness.
func (h *Host) EvaluateCloseReady(ctx context.Context) {
	hdr, hdrErr := h.latestHeader(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closeReady == nil {
		return
	}
	last, degraded := h.sm.HeightSyncArmingContext()
	h.closeReady.SetTurnContext(last, degraded)
	hNow, _ := headerStamp(hdr, hdrErr)
	h.closeReady.Evaluate(hNow)
}

// CloseReadyView is the §12 producer. Never nil after NewHost.
func (h *Host) CloseReadyView() heightsync.CloseReadyView {
	return h.closeReady
}

// CloseReadyIntervals is the retained [armed_at, disarmed_at) list (spec §12).
func (h *Host) CloseReadyIntervals() []heightsync.ArmedInterval {
	if h.closeReady == nil {
		return nil
	}
	return h.closeReady.Intervals()
}

func contactHeight(tx *types.DevshardTx) uint64 {
	if tx == nil {
		return 0
	}
	if hb := tx.GetHeartbeat(); hb != nil && hb.ObservedHeight > 0 {
		return hb.ObservedHeight
	}
	if h, ok := heightsync.TxStamp(tx); ok {
		return h
	}
	return 0
}
