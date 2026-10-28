package storage

import (
	"fmt"
	"sort"

	"devshard/types"
)

type pagedValidationObsRebuild struct {
	store    Storage
	escrowID string
	pending  []ValidationObsEntry
}

func (rebuild *pagedValidationObsRebuild) add(records []types.DiffRecord) error {
	for _, record := range records {
		entries := ValidationObsEntriesFromTxs(record.Txs)
		if len(entries) == 0 {
			continue
		}
		rebuild.pending = append(rebuild.pending, entries...)
		if len(rebuild.pending) >= validationObsRebuildChunk {
			if err := rebuild.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (rebuild *pagedValidationObsRebuild) flush() error {
	if len(rebuild.pending) == 0 {
		return nil
	}
	if err := rebuild.store.RecordValidationsAppliedOnce(rebuild.escrowID, rebuild.pending); err != nil {
		return fmt.Errorf("validation obs rebuild: record: %w", err)
	}
	rebuild.pending = rebuild.pending[:0]
	return nil
}

// RebuildValidationObs is RebuildValidationObsFromDiffs over a journal eachPage feeds to add page by page, in nonce order; a partial journal double-counts.
func RebuildValidationObs(store Storage, escrowID string, eachPage func(add func([]types.DiffRecord) error) error, sealedInferenceIDs []uint64) error {
	if store == nil {
		return fmt.Errorf("validation obs rebuild: nil store")
	}
	if err := store.ClearValidationObs(escrowID); err != nil {
		return fmt.Errorf("validation obs rebuild: clear: %w", err)
	}
	rebuild := &pagedValidationObsRebuild{
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
	inferenceIDs := append([]uint64(nil), sealedInferenceIDs...)
	sort.Slice(inferenceIDs, func(left, right int) bool { return inferenceIDs[left] < inferenceIDs[right] })
	if err := store.DrainInferenceValidationObsBatch(escrowID, inferenceIDs); err != nil {
		return fmt.Errorf("validation obs rebuild: drain: %w", err)
	}
	return nil
}
