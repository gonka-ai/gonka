package storage

import (
	"fmt"
	"sort"

	"devshard/types"
)

// ValidationObsEntriesFromTxs collects distinct (inference_id, slot_id) pairs
// from validation and validation-vote txs in a diff.
func ValidationObsEntriesFromTxs(txs []*types.DevshardTx) []ValidationObsEntry {
	entries := make([]ValidationObsEntry, 0, len(txs))
	seen := make(map[ValidationObsEntry]struct{}, len(txs))
	add := func(e ValidationObsEntry) {
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
			add(ValidationObsEntry{InferenceID: v.InferenceId, SlotID: v.ValidatorSlot})
		case tx.GetValidationVote() != nil:
			v := tx.GetValidationVote()
			add(ValidationObsEntry{InferenceID: v.InferenceId, SlotID: v.VoterSlot})
		}
	}
	return entries
}

// RebuildValidationObsFromDiffs rebuilds validation observability for an escrow
// from the canonical diff journal. It clears live and sealed obs tables, replays
// validation txs from records in nonce order, then drains live rows for each
// sealed inference id. Idempotent w.r.t. diff content.
func RebuildValidationObsFromDiffs(store Storage, escrowID string, records []types.DiffRecord, sealedInferenceIDs []uint64) error {
	if store == nil {
		return fmt.Errorf("validation obs rebuild: nil store")
	}
	if err := store.ClearValidationObs(escrowID); err != nil {
		return fmt.Errorf("validation obs rebuild: clear: %w", err)
	}
	for _, rec := range records {
		entries := ValidationObsEntriesFromTxs(rec.Txs)
		if len(entries) == 0 {
			continue
		}
		if err := store.RecordValidationsAppliedOnce(escrowID, entries); err != nil {
			return fmt.Errorf("validation obs rebuild: record nonce %d: %w", rec.Nonce, err)
		}
	}
	ids := append([]uint64(nil), sealedInferenceIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, inferenceID := range ids {
		if err := store.DrainInferenceValidationObs(escrowID, inferenceID); err != nil {
			return fmt.Errorf("validation obs rebuild: drain inference %d: %w", inferenceID, err)
		}
	}
	return nil
}

// SealedInferenceIDsSorted returns sorted inference ids from a seal-nonce map.
func SealedInferenceIDsSorted(sealedNonces map[uint64]uint64) []uint64 {
	if len(sealedNonces) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(sealedNonces))
	for id := range sealedNonces {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
