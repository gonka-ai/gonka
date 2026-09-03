package storage

import (
	"fmt"
	"slices"

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

// validationObsRebuildChunk bounds entries per record write and ids per drain
// transaction during a rebuild. Same rationale as sealedInferenceInsertChunk:
// keep each commit inside the Postgres statement timeout without paying a
// round trip per journal record or a transaction per inference.
const validationObsRebuildChunk = 500

// RebuildValidationObsFromDiffs rebuilds validation observability for an escrow
// from the canonical diff journal. It clears live and sealed obs tables, replays
// validation txs from records in nonce order, then drains live rows for each
// sealed inference id. Idempotent w.r.t. diff content.
//
// Only the clear makes this safe to re-run: the drain deletes the live row
// that RecordValidationsAppliedOnce dedups against, so replaying a range on top
// of already-drained rows would count those validations a second time. Callers
// must pass the whole journal, never a partial range.
func RebuildValidationObsFromDiffs(store Storage, escrowID string, records []types.DiffRecord, sealedInferenceIDs []uint64) error {
	if store == nil {
		return fmt.Errorf("validation obs rebuild: nil store")
	}
	if err := store.ClearValidationObs(escrowID); err != nil {
		return fmt.Errorf("validation obs rebuild: clear: %w", err)
	}
	// Accumulate across records instead of writing once per nonce: the write is
	// ON CONFLICT DO NOTHING keyed on (inference_id, slot_id), so merging
	// records is indistinguishable from applying them one at a time.
	pending := make([]ValidationObsEntry, 0, validationObsRebuildChunk)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := store.RecordValidationsAppliedOnce(escrowID, pending); err != nil {
			return fmt.Errorf("validation obs rebuild: record: %w", err)
		}
		pending = pending[:0]
		return nil
	}
	for _, rec := range records {
		entries := ValidationObsEntriesFromTxs(rec.Txs)
		if len(entries) == 0 {
			continue
		}
		pending = append(pending, entries...)
		if len(pending) >= validationObsRebuildChunk {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}

	ids := append([]uint64(nil), sealedInferenceIDs...)
	slices.Sort(ids)
	if err := store.DrainInferenceValidationObsBatch(escrowID, ids); err != nil {
		return fmt.Errorf("validation obs rebuild: drain: %w", err)
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
	slices.Sort(out)
	return out
}
