package storage

import "devshard/logging"

// RecordValidationRequired increments the observability required-validation counter
// for an inference when this host schedules a validation job.
func RecordValidationRequired(store Storage, escrowID string, inferenceID uint64, slotID uint32) {
	if store == nil {
		return
	}
	if err := store.IncrInferenceValidationObs(escrowID, inferenceID, slotID, 1, 0); err != nil {
		logging.Debug("record validation required failed",
			"subsystem", "storage",
			"escrow_id", escrowID,
			"slot_id", slotID,
			"error", err,
		)
	}
}

// RecordValidationCompleted increments the observability completed-validation counter
// when this host publishes a validation or validation-vote tx to its mempool.
func RecordValidationCompleted(store Storage, escrowID string, inferenceID uint64, slotID uint32) {
	if store == nil {
		return
	}
	if err := store.IncrInferenceValidationObs(escrowID, inferenceID, slotID, 0, 1); err != nil {
		logging.Debug("record validation completed failed",
			"subsystem", "storage",
			"escrow_id", escrowID,
			"slot_id", slotID,
			"error", err,
		)
	}
}
