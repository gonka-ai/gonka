package accounting

import (
	"fmt"
	"maps"

	"devshard/types"
)

func (b *Book) ObserveLatestNonce(escrowID string, nonce uint64) error {
	return b.observeEscrow(escrowID, func(escrow *escrowLedger) bool {
		escrow.pendingSweepWatermark = max(escrow.pendingSweepWatermark, nonce)
		return escrow.raiseLatest(nonce)
	})
}

func (b *Book) ObserveHostStats(escrowID string, slotID uint32, stats types.HostStats) error {
	return b.observeEscrow(escrowID, func(escrow *escrowLedger) bool {
		held, seen := escrow.hostStats[slotID]
		if !seen && escrow.timeouts[slotID] == 0 {
			escrow.timeouts[slotID] = uint64(stats.Missed)
		}
		escrow.hostStats[slotID] = stats
		return !seen || held != stats
	})
}

// ObserveInferences replaces an escrow's per-nonce money and its open-challenge counts from one sweep.
func (b *Book) ObserveInferences(escrowID string, inferences map[uint64]*types.InferenceRecord) error {
	return b.observeEscrow(escrowID, func(escrow *escrowLedger) bool {
		folded := escrow.foldSealed(inferences)
		changed := folded
		challenged := make(map[uint32]uint64)
		var inFlight, challengedCost uint64
		for nonce, record := range inferences {
			if record == nil {
				continue
			}
			if FinishApplied(record.Status) && escrow.markFinished(nonce) {
				changed = true
			}
			cost := costOf(record)
			if changed {
				escrow.costs[nonce] = cost
			} else if held, known := escrow.costs[nonce]; !known || held != cost {
				escrow.costs[nonce] = cost
				changed = true
			}
			if record.Status == types.StatusChallenged {
				challenged[record.ExecutorSlot]++
			}
			switch record.Status {
			case types.StatusPending, types.StatusStarted:
				inFlight += record.ReservedCost
			case types.StatusChallenged:
				challengedCost += record.ActualCost
			}
		}
		changed = changed || !maps.Equal(escrow.challenged, challenged) ||
			escrow.inFlightReserved != inFlight || escrow.challengedCost != challengedCost
		escrow.challenged = challenged
		escrow.inFlightReserved, escrow.challengedCost = inFlight, challengedCost
		escrow.appliedSweepWatermark = escrow.pendingSweepWatermark
		if escrow.dropSealedInputCounts() {
			changed = true
		}
		if folded && escrow.retired {
			escrow.shrinkPerNonceMaps()
		}
		return changed
	})
}

// ObserveCharging takes the session's frozen fee per nonce and the chain's finalize nonce, from which no nonce pays it.
func (b *Book) ObserveCharging(escrowID string, feePerNonce, finalizeNonce uint64) error {
	return b.observeEscrow(escrowID, func(escrow *escrowLedger) bool {
		changed := escrow.feePerNonce != feePerNonce
		escrow.feePerNonce = feePerNonce
		return escrow.noteFinalizeStart(finalizeNonce) || changed
	})
}

func (b *Book) observeEscrow(escrowID string, observe func(*escrowLedger) bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	escrow, known := b.escrows[escrowID]
	if !known {
		return fmt.Errorf("%w: %s", ErrUnknownEscrow, escrowID)
	}
	if observe(escrow) {
		b.markUnsavedLocked(escrowID)
	}
	b.noteUpdateLocked()
	return nil
}

func (e *escrowLedger) shrinkPerNonceMaps() {
	e.costs = shrinkToFit(e.costs)
	e.nonces = shrinkToFit(e.nonces)
	e.inputCounts = shrinkToFit(e.inputCounts)
}

func shrinkToFit[Key comparable, Value any](source map[Key]Value) map[Key]Value {
	fresh := make(map[Key]Value, len(source))
	maps.Copy(fresh, source)
	return fresh
}
