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

// validationObsRebuildChunk bounds entries per record write and ids per drain
// transaction during a rebuild. Same rationale as sealedInferenceInsertChunk:
// keep each commit inside the Postgres statement timeout without paying a
// round trip per journal record or a transaction per inference.
const validationObsRebuildChunk = 500

type validationObsRebuild struct {
	store    Storage
	escrowID string
	pending  []ValidationObsEntry
}

// add merges entries across records instead of writing once per nonce; the write is ON CONFLICT DO NOTHING.
func (r *validationObsRebuild) add(records []types.DiffRecord) error {
	for _, record := range records {
		entries := ValidationObsEntriesFromTxs(record.Txs)
		if len(entries) == 0 {
			continue
		}
		r.pending = append(r.pending, entries...)
		if len(r.pending) >= validationObsRebuildChunk {
			if err := r.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *validationObsRebuild) flush() error {
	if len(r.pending) == 0 {
		return nil
	}
	if err := r.store.RecordValidationsAppliedOnce(r.escrowID, r.pending); err != nil {
		return fmt.Errorf("validation obs rebuild: record: %w", err)
	}
	r.pending = r.pending[:0]
	return nil
}

// RebuildValidationObs clears validation observability for an escrow, replays the whole diff journal that
// eachPage feeds to add in nonce order, then drains live rows of sealed inferences; a partial journal double-counts.
func RebuildValidationObs(store Storage, escrowID string, eachPage func(add func([]types.DiffRecord) error) error, sealedInferenceIDs []uint64) error {
	if store == nil {
		return fmt.Errorf("validation obs rebuild: nil store")
	}
	if err := store.ClearValidationObs(escrowID); err != nil {
		return fmt.Errorf("validation obs rebuild: clear: %w", err)
	}
	rebuild := &validationObsRebuild{
		store:    store,
		escrowID: escrowID,
		pending:  make([]ValidationObsEntry, 0, validationObsRebuildChunk),
	}
	if err := eachPage(rebuild.add); err != nil {
		return err
	}
	if err := rebuild.flush(); err != nil {
		return err
	}
	ids := append([]uint64(nil), sealedInferenceIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if err := store.DrainInferenceValidationObsBatch(escrowID, ids); err != nil {
		return fmt.Errorf("validation obs rebuild: drain: %w", err)
	}
	return nil
}

// RebuildValidationObsFromDiffs is RebuildValidationObs over a journal already held in memory.
func RebuildValidationObsFromDiffs(store Storage, escrowID string, records []types.DiffRecord, sealedInferenceIDs []uint64) error {
	return RebuildValidationObs(store, escrowID, func(add func([]types.DiffRecord) error) error {
		return add(records)
	}, sealedInferenceIDs)
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
