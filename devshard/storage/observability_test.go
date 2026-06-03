package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInferenceValidationObs_DrainMovesToSealed(t *testing.T) {
	store := NewMemory()
	require.NoError(t, store.CreateSession(CreateSessionParams{EscrowID: "escrow-1", EpochID: 1, Version: "test"}))

	require.NoError(t, store.IncrInferenceValidationObs("escrow-1", 7, 2, 3, 1))
	require.NoError(t, store.IncrInferenceValidationObs("escrow-1", 7, 2, 0, 2))
	require.NoError(t, store.DrainInferenceValidationObs("escrow-1", 7))

	rows, err := store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(2), rows[0].SlotID)
	require.Equal(t, uint32(3), rows[0].RequiredValidations)
	require.Equal(t, uint32(3), rows[0].CompletedValidations)

	// Live counters for this inference are gone; totals still visible from sealed storage.
	require.NoError(t, store.IncrInferenceValidationObs("escrow-1", 9, 2, 1, 0))
	rows, err = store.GetValidationObservability("escrow-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(4), rows[0].RequiredValidations)
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
	}
	require.NoError(t, store.InsertSealedInference("escrow-1", want))

	row, ok, err := store.GetSealedInference("escrow-1", 7)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want, row)
}
