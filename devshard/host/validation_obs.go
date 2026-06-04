package host

import (
	"devshard/logging"
	"devshard/storage"
	"devshard/types"
)

const validationObsInFlightCap = 64

// extractValidationObsEntries collects distinct (inference_id, slot_id) pairs
// from validation and validation-vote txs in an applied diff. Duplicates within
// the diff are dropped here so the batch write is minimal and does not lean on
// the store's ON CONFLICT path for intra-batch dedup.
func extractValidationObsEntries(txs []*types.DevshardTx) []storage.ValidationObsEntry {
	entries := make([]storage.ValidationObsEntry, 0, len(txs))
	seen := make(map[storage.ValidationObsEntry]struct{}, len(txs))
	add := func(e storage.ValidationObsEntry) {
		if _, ok := seen[e]; ok {
			return
		}
		seen[e] = struct{}{}
		entries = append(entries, e)
	}
	for _, tx := range txs {
		switch {
		case tx.GetValidation() != nil:
			v := tx.GetValidation()
			add(storage.ValidationObsEntry{InferenceID: v.InferenceId, SlotID: v.ValidatorSlot})
		case tx.GetValidationVote() != nil:
			v := tx.GetValidationVote()
			add(storage.ValidationObsEntry{InferenceID: v.InferenceId, SlotID: v.VoterSlot})
		}
	}
	return entries
}

// writeValidationObsBatch persists observability rows. Best-effort: logs and
// continues on storage errors.
func writeValidationObsBatch(store storage.Storage, escrowID string, entries []storage.ValidationObsEntry) {
	if store == nil || len(entries) == 0 {
		return
	}
	if err := store.RecordValidationsAppliedOnce(escrowID, entries); err != nil {
		logging.Debug("record validation obs batch failed",
			"subsystem", "host",
			"escrow_id", escrowID,
			"entries", len(entries),
			"error", err,
		)
	}
}

// recordValidationObsFromAppliedDiff extracts entries under lock and dispatches
// a batched write off-lock. Correctness depends on ApplyDiff rejecting
// late/sealed validations before this runs; do not move recording before
// ApplyDiff.
func (h *Host) recordValidationObsFromAppliedDiff(txs []*types.DevshardTx) {
	if h.store == nil {
		return
	}
	entries := extractValidationObsEntries(txs)
	if len(entries) == 0 {
		return
	}
	store := h.store
	escrowID := h.escrowID

	if h.validationObsInFlight.Add(1) > validationObsInFlightCap {
		h.validationObsInFlight.Add(-1)
		// Backpressure: too many async obs writes already in flight (a slow or
		// stalled store). Drop this batch rather than writing synchronously under
		// h.mu, which would re-serialize the hot path onto a slow store.
		// Observability is best-effort: diff replay re-records it idempotently on
		// the next boot via INSERT ... ON CONFLICT DO NOTHING.
		logging.Warn("validation obs async cap reached; dropping batch (best-effort, replay re-records)",
			"subsystem", "host",
			"escrow_id", escrowID,
			"in_flight_cap", validationObsInFlightCap,
			"entries", len(entries),
		)
		return
	}

	go func() {
		defer h.validationObsInFlight.Add(-1)
		writeValidationObsBatch(store, escrowID, entries)
	}()
}
