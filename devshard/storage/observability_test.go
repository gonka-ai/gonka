package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func setupObsTestStore(t *testing.T) Storage {
	t.Helper()
	store := NewMemory()
	require.NoError(t, store.CreateSession(CreateSessionParams{EscrowID: "escrow-1", EpochID: 1, Version: "test"}))
	return store
}

func obsForSlot(t *testing.T, store Storage, slotID uint32) SlotValidationObs {
	t.Helper()
	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	for _, r := range rows {
		if r.SlotID == slotID {
			return r
		}
	}
	return SlotValidationObs{SlotID: slotID}
}

// recordOnce is a single-entry convenience over the batch obs API for tests.
func recordOnce(t *testing.T, store Storage, escrowID string, inferenceID uint64, slotID uint32) {
	t.Helper()
	require.NoError(t, store.RecordValidationsAppliedOnce(escrowID, []ValidationObsEntry{
		{InferenceID: inferenceID, SlotID: slotID},
	}))
}

func TestRecordValidationsAppliedOnce_FirstCallIncrements(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 2)

	obs := obsForSlot(t, store, 2)
	require.Equal(t, uint32(1), obs.RequiredValidations)
	require.Equal(t, uint32(1), obs.CompletedValidations)
}

func TestRecordValidationsAppliedOnce_SecondCallNoIncrement(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 2)
	recordOnce(t, store, "escrow-1", 7, 2)

	obs := obsForSlot(t, store, 2)
	require.Equal(t, uint32(1), obs.RequiredValidations)
	require.Equal(t, uint32(1), obs.CompletedValidations)
}

func TestRecordValidationsAppliedOnce_DifferentSlotsBothIncrement(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 1)
	recordOnce(t, store, "escrow-1", 7, 2)

	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestRecordValidationsAppliedOnce_DifferentInferencesSameSlot(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 2)
	recordOnce(t, store, "escrow-1", 9, 2)

	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(2), rows[0].RequiredValidations)
	require.Equal(t, uint32(2), rows[0].CompletedValidations)
}

func TestRecordValidationsAppliedOnce_DrainThenReinsertReappears(t *testing.T) {
	store := setupObsTestStore(t)

	recordOnce(t, store, "escrow-1", 7, 2)

	require.NoError(t, store.DrainInferenceValidationObs("escrow-1", 7))

	recordOnce(t, store, "escrow-1", 7, 2)

	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(2), rows[0].RequiredValidations)
	require.Equal(t, uint32(2), rows[0].CompletedValidations)
}

func TestInferenceValidationObs_DrainMovesToSealed(t *testing.T) {
	store := NewMemory()
	require.NoError(t, store.CreateSession(CreateSessionParams{EscrowID: "escrow-1", EpochID: 1, Version: "test"}))

	recordOnce(t, store, "escrow-1", 7, 2)
	require.NoError(t, store.DrainInferenceValidationObs("escrow-1", 7))

	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(2), rows[0].SlotID)
	require.Equal(t, uint32(1), rows[0].RequiredValidations)
	require.Equal(t, uint32(1), rows[0].CompletedValidations)

	// Live counters for inference 7 are gone; totals still visible from sealed storage.
	recordOnce(t, store, "escrow-1", 9, 2)
	rows, err = store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(2), rows[0].RequiredValidations)
	require.Equal(t, uint32(2), rows[0].CompletedValidations)
}

func TestRecordValidationsAppliedOnce_BatchInsertsAll(t *testing.T) {
	store := setupObsTestStore(t)

	require.NoError(t, store.RecordValidationsAppliedOnce("escrow-1", []ValidationObsEntry{
		{InferenceID: 7, SlotID: 1},
		{InferenceID: 7, SlotID: 2},
		{InferenceID: 9, SlotID: 2},
	}))

	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, uint32(1), obsForSlot(t, store, 1).CompletedValidations)
	require.Equal(t, uint32(2), obsForSlot(t, store, 2).CompletedValidations)
}

func TestRecordValidationsAppliedOnce_BatchDedupDuplicateEntry(t *testing.T) {
	store := setupObsTestStore(t)

	require.NoError(t, store.RecordValidationsAppliedOnce("escrow-1", []ValidationObsEntry{
		{InferenceID: 7, SlotID: 2},
		{InferenceID: 7, SlotID: 2},
	}))

	obs := obsForSlot(t, store, 2)
	require.Equal(t, uint32(1), obs.RequiredValidations)
	require.Equal(t, uint32(1), obs.CompletedValidations)
}

func TestInferenceRow_ObsSnapshotRoundTrip(t *testing.T) {
	store := NewMemory()
	require.NoError(t, store.CreateSession(CreateSessionParams{EscrowID: "escrow-1", EpochID: 1, Version: "test"}))

	want := InferenceRow{
		InferenceID:        7,
		SealedNonce:        9,
		ObsPresent:         true,
		SealedStatus:       3,
		SealedExecutorSlot: 2,
		SealedVotesValid:   1,
		SealedVotesInvalid: 4,
		SealedValidatedBy:  []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		SealedModel:        "llama",
		SealedPromptHash:   []byte("prompt"),
		SealedResponseHash: []byte("response"),
		SealedInputLength:  100,
		SealedMaxTokens:    50,
		SealedInputTokens:  10,
		SealedOutputTokens: 20,
		SealedReservedCost: 500,
		SealedActualCost:   300,
		SealedStartedAt:    1000,
		SealedConfirmedAt:  2000,
	}
	require.NoError(t, store.InsertSealedInference("escrow-1", want))

	row, ok, err := store.GetSealedInference("escrow-1", 7)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, row)
}
