package storage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

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

func TestValidationObsEntriesFromTxs_DedupWithinDiff(t *testing.T) {
	txs := []*types.DevshardTx{validationTx(7, 2), validationTx(7, 2)}
	entries := ValidationObsEntriesFromTxs(txs)
	require.Len(t, entries, 1)
	require.Equal(t, ValidationObsEntry{InferenceID: 7, SlotID: 2}, entries[0])
}
