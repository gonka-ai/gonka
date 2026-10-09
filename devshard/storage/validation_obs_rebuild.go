package storage

import (
	"errors"
	"fmt"
	"sort"

	"devshard/types"
)

// ErrValidationObsRebuildBusy is returned when another process holds the
// escrow's rebuild lock. Its rebuild clears the pending mark when it finishes;
// if it dies, the mark stays set for the next recovery.
var ErrValidationObsRebuildBusy = errors.New("validation obs rebuild already running")

// RunValidationObsRebuild runs a full obs rebuild under the escrow's rebuild
// lock and the durable pending mark. The mark is set before rebuild runs, so
// before anything clears the obs rows, and is removed only when rebuild
// returns nil. A failed or interrupted rebuild leaves the mark set, and
// recovery that finds it repeats the rebuild instead of trusting the rows.
func RunValidationObsRebuild(store Storage, escrowID string, rebuild func() error) error {
	if store == nil {
		return fmt.Errorf("validation obs rebuild: nil store")
	}
	unlock, acquired, err := store.LockValidationObsRebuild(escrowID)
	if err != nil {
		return fmt.Errorf("validation obs rebuild: lock: %w", err)
	}
	if !acquired {
		return ErrValidationObsRebuildBusy
	}
	defer unlock()
	if err := store.SetValidationObsRebuildPending(escrowID, true); err != nil {
		return fmt.Errorf("validation obs rebuild: mark pending: %w", err)
	}
	if err := rebuild(); err != nil {
		return err
	}
	if err := store.SetValidationObsRebuildPending(escrowID, false); err != nil {
		return fmt.Errorf("validation obs rebuild: mark complete: %w", err)
	}
	return nil
}

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
	return rebuildValidationObs(store, escrowID, sealedInferenceIDs, func(record func([]types.DiffRecord) error) error {
		return record(records)
	})
}

// RebuildValidationObsFromJournal clears the obs tables once, then walks
// [from, to] through ReadDiffPages. Each page is recorded with the 500-entry
// flush and dropped. A partial page is never passed to
// RebuildValidationObsFromDiffs, which would clear again. from > to still
// clears and drains, and does not read the store. onPage, when set, sees each
// page before it is dropped and must not retain the slice.
func RebuildValidationObsFromJournal(store Storage, escrowID string, from, to uint64, sealedInferenceIDs []uint64, onPage func([]types.DiffRecord) error) error {
	return rebuildValidationObs(store, escrowID, sealedInferenceIDs, func(record func([]types.DiffRecord) error) error {
		if from > to {
			return nil
		}
		return ReadDiffPages(store, escrowID, from, to, func(page []types.DiffRecord) error {
			if onPage != nil {
				if err := onPage(page); err != nil {
					return err
				}
			}
			return record(page)
		})
	})
}

func rebuildValidationObs(store Storage, escrowID string, sealedInferenceIDs []uint64, walk func(func([]types.DiffRecord) error) error) error {
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
	if err := walk(func(records []types.DiffRecord) error {
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
		return nil
	}); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}

	ids := append([]uint64(nil), sealedInferenceIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
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
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
