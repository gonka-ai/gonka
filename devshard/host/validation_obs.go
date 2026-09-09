package host

import (
	"devshard/logging"
	"devshard/storage"
	"devshard/types"
)

const validationObsInFlightCap = 64

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
	entries := storage.ValidationObsEntriesFromTxs(txs)
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
		// Observability is best-effort: recovery rebuilds from the diff journal.
		logging.Warn("validation obs async cap reached; dropping batch (best-effort, recovery rebuilds from diffs)",
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
