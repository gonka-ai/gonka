package storage

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// pagedReadStore fails a GetDiffs that spans more than one page of nonces.
type pagedReadStore struct {
	Storage
	mu    sync.Mutex
	calls [][2]uint64
}

func (s *pagedReadStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	s.mu.Lock()
	s.calls = append(s.calls, [2]uint64{from, to})
	s.mu.Unlock()
	if to < from || to-from >= DiffPageMaxNonces {
		return nil, fmt.Errorf("GetDiffs span %d..%d", from, to)
	}
	return s.Storage.GetDiffs(escrowID, from, to)
}

func (s *pagedReadStore) ranges() [][2]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][2]uint64, len(s.calls))
	copy(out, s.calls)
	return out
}

func validationTx(inferenceID uint64, slotID uint32) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{
		InferenceId:   inferenceID,
		ValidatorSlot: slotID,
		EscrowId:      "escrow-1",
	}}}
}

func TestDeleteSealedInferences_PreservesValidationObs(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 2)
	require.NoError(t, store.DrainInferenceValidationObs("escrow-1", 7))

	before, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, uint32(1), before[0].CompletedValidations)

	require.NoError(t, store.DeleteSealedInferences("escrow-1"))

	after, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestRebuildValidationObsFromDiffs_LiveInference(t *testing.T) {
	store := setupObsTestStore(t)

	records := []types.DiffRecord{{
		Diff: types.Diff{
			Nonce: 1,
			Txs:   []*types.DevshardTx{validationTx(7, 2)},
		},
	}}
	require.NoError(t, RebuildValidationObsFromDiffs(store, "escrow-1", records, nil))

	obs := obsForSlot(t, store, 2)
	require.Equal(t, uint32(1), obs.RequiredValidations)
	require.Equal(t, uint32(1), obs.CompletedValidations)
}

func TestRebuildValidationObsFromDiffs_SealedInference(t *testing.T) {
	store := setupObsTestStore(t)

	records := []types.DiffRecord{{
		Diff: types.Diff{
			Nonce: 1,
			Txs:   []*types.DevshardTx{validationTx(7, 2)},
		},
	}}
	require.NoError(t, RebuildValidationObsFromDiffs(store, "escrow-1", records, []uint64{7}))

	obs := obsForSlot(t, store, 2)
	require.Equal(t, uint32(1), obs.RequiredValidations)
	require.Equal(t, uint32(1), obs.CompletedValidations)
}

func TestRebuildValidationObsFromDiffs_Idempotent(t *testing.T) {
	store := setupObsTestStore(t)

	records := []types.DiffRecord{{
		Diff: types.Diff{
			Nonce: 1,
			Txs:   []*types.DevshardTx{validationTx(7, 2), validationTx(7, 2)},
		},
	}}
	require.NoError(t, RebuildValidationObsFromDiffs(store, "escrow-1", records, []uint64{7}))
	want, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)

	require.NoError(t, RebuildValidationObsFromDiffs(store, "escrow-1", records, []uint64{7}))
	got, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestRebuildValidationObsFromDiffs_ReplacesPriorState(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 2)
	recordOnce(t, store, "escrow-1", 9, 2)

	records := []types.DiffRecord{{
		Diff: types.Diff{
			Nonce: 1,
			Txs:   []*types.DevshardTx{validationTx(7, 2)},
		},
	}}
	require.NoError(t, RebuildValidationObsFromDiffs(store, "escrow-1", records, nil))

	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(1), rows[0].CompletedValidations)
}

// Recording is only deduped while the live row survives: the drain deletes it,
// so re-recording an inference that already sealed counts it a second time.
// This is why the rebuild has to clear first and why recovery must never
// replay a partial range of diffs on top of stored obs.
func TestRecordValidationsAppliedOnce_NotDedupedAfterDrain(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 2)
	require.NoError(t, store.DrainInferenceValidationObs("escrow-1", 7))
	require.Equal(t, uint32(1), obsForSlot(t, store, 2).CompletedValidations)

	recordOnce(t, store, "escrow-1", 7, 2)
	require.NoError(t, store.DrainInferenceValidationObs("escrow-1", 7))
	require.Equal(t, uint32(2), obsForSlot(t, store, 2).CompletedValidations,
		"the drain removes the dedup row, so a repeated record double counts")
}

func TestRebuildValidationObsFromJournal_MatchesSliceRebuild(t *testing.T) {
	records := []types.DiffRecord{
		{Diff: types.Diff{Nonce: 1, Txs: []*types.DevshardTx{validationTx(7, 2), validationTx(7, 2), validationTx(8, 1)}}},
		{Diff: types.Diff{Nonce: 2, Txs: []*types.DevshardTx{validationTx(7, 3)}}},
		{Diff: types.Diff{Nonce: 3, Txs: []*types.DevshardTx{validationTx(9, 2)}}},
	}
	sealed := []uint64{7}

	sliceStore := setupObsTestStore(t)
	for _, rec := range records {
		require.NoError(t, sliceStore.AppendDiff("escrow-1", rec))
	}
	require.NoError(t, RebuildValidationObsFromDiffs(sliceStore, "escrow-1", records, sealed))
	want, err := sliceStore.GetValidationObservability("escrow-1")
	require.NoError(t, err)

	journalStore := setupObsTestStore(t)
	for _, rec := range records {
		require.NoError(t, journalStore.AppendDiff("escrow-1", rec))
	}
	paged := &pagedReadStore{Storage: journalStore}
	require.NoError(t, RebuildValidationObsFromJournal(paged, "escrow-1", 1, 3, sealed, nil))
	got, err := journalStore.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, [][2]uint64{{1, 3}}, paged.ranges(), "a three-nonce journal is one page")
}

func TestRebuildValidationObsFromJournal_EmptyRangeClearsWithoutReading(t *testing.T) {
	store := setupObsTestStore(t)
	recordOnce(t, store, "escrow-1", 7, 2)
	paged := &pagedReadStore{Storage: store}
	require.NoError(t, RebuildValidationObsFromJournal(paged, "escrow-1", 1, 0, nil, nil))
	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Empty(t, paged.ranges())
}

// rebuildMarkStore can refuse the rebuild lock or the pending write, to cover
// the paths where RunValidationObsRebuild must not start a rebuild.
type rebuildMarkStore struct {
	Storage
	lockHeld    bool
	failPending bool
	unlocks     int
}

func (s *rebuildMarkStore) LockValidationObsRebuild(escrowID string) (func(), bool, error) {
	if s.lockHeld {
		return nil, false, nil
	}
	return func() { s.unlocks++ }, true, nil
}

func (s *rebuildMarkStore) SetValidationObsRebuildPending(escrowID string, pending bool) error {
	if s.failPending {
		return fmt.Errorf("pending write refused")
	}
	return s.Storage.SetValidationObsRebuildPending(escrowID, pending)
}

func requireRebuildPending(t *testing.T, store Storage, want bool) {
	t.Helper()
	pending, err := store.ValidationObsRebuildPending("escrow-1")
	require.NoError(t, err)
	require.Equal(t, want, pending)
}

func TestRunValidationObsRebuild_ClearsMarkOnlyOnSuccess(t *testing.T) {
	store := &rebuildMarkStore{Storage: setupObsTestStore(t)}

	err := RunValidationObsRebuild(store, "escrow-1", func() error {
		requireRebuildPending(t, store, true)
		return fmt.Errorf("page 2 read failed")
	})
	require.ErrorContains(t, err, "page 2 read failed")
	requireRebuildPending(t, store, true)
	require.Equal(t, 1, store.unlocks, "a failed rebuild still releases the lock")

	require.NoError(t, RunValidationObsRebuild(store, "escrow-1", func() error { return nil }))
	requireRebuildPending(t, store, false)
	require.Equal(t, 2, store.unlocks)
}

func TestRunValidationObsRebuild_BusyLockDoesNotRebuild(t *testing.T) {
	store := &rebuildMarkStore{Storage: setupObsTestStore(t), lockHeld: true}
	require.NoError(t, store.Storage.SetValidationObsRebuildPending("escrow-1", true))

	err := RunValidationObsRebuild(store, "escrow-1", func() error {
		t.Fatal("rebuild must not run without the lock")
		return nil
	})
	require.ErrorIs(t, err, ErrValidationObsRebuildBusy)
	requireRebuildPending(t, store, true)
}

// The mark is written before the rebuild's clear. If it cannot be written,
// the rows must stay as they are, or a crash would lose them unrecorded.
func TestRunValidationObsRebuild_UnwritableMarkDoesNotClear(t *testing.T) {
	inner := setupObsTestStore(t)
	recordOnce(t, inner, "escrow-1", 7, 2)
	store := &rebuildMarkStore{Storage: inner, failPending: true}

	err := RunValidationObsRebuild(store, "escrow-1", func() error {
		return RebuildValidationObsFromJournal(inner, "escrow-1", 1, 0, nil, nil)
	})
	require.ErrorContains(t, err, "mark pending")
	rows, err := inner.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1, "obs rows survive a refused mark")
}

func TestValidationObsEntriesFromTxs_DedupWithinDiff(t *testing.T) {
	txs := []*types.DevshardTx{validationTx(7, 2), validationTx(7, 2)}
	entries := ValidationObsEntriesFromTxs(txs)
	require.Len(t, entries, 1)
	require.Equal(t, ValidationObsEntry{InferenceID: 7, SlotID: 2}, entries[0])
}
