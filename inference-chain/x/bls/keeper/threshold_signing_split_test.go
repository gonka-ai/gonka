package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// TestStoreThresholdSigningRequest_StripsAndSyncsPartialSignatures pins the
// invariant that storeThresholdSigningRequest must (a) persist any inline
// PartialSignature entries to per-submitter sub-keys and (b) zero the
// inline slice in the base struct so later writes stay constant-size.
// Mirrors the EpochBLSData split — see
// ThresholdPartialSigRequestPrefix for the gas-scaling rationale.
func TestStoreThresholdSigningRequest_StripsAndSyncsPartialSignatures(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	requestID := []byte("req-split-test")
	req := &types.ThresholdSigningRequest{
		RequestId: requestID,
		Status:    types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
		PartialSignatures: []types.PartialSignature{
			{ParticipantAddress: "p1", SlotIndices: []uint32{1, 2}, Signature: make([]byte, 96)},
			{ParticipantAddress: "p2", SlotIndices: []uint32{3}, Signature: make([]byte, 48)},
		},
	}
	require.NoError(t, k.storeThresholdSigningRequest(ctx, req))

	// Base struct on disk must carry zero inline partial signatures.
	rawStore := k.storeService.OpenKVStore(ctx)
	rawBz, err := rawStore.Get(types.ThresholdSigningRequestKey(requestID))
	require.NoError(t, err)
	var persisted types.ThresholdSigningRequest
	require.NoError(t, k.cdc.Unmarshal(rawBz, &persisted))
	require.Empty(t, persisted.PartialSignatures,
		"base struct must have zero inline partial sigs or the N^2 write-per-byte bug returns")

	// Each entry must be retrievable through its sub-key.
	ps1, err := k.GetThresholdPartialSignature(ctx, requestID, "p1")
	require.NoError(t, err)
	require.NotNil(t, ps1)
	require.Equal(t, []uint32{1, 2}, ps1.SlotIndices)

	ps2, err := k.GetThresholdPartialSignature(ctx, requestID, "p2")
	require.NoError(t, err)
	require.NotNil(t, ps2)
	require.Equal(t, []uint32{3}, ps2.SlotIndices)

	// HasThresholdPartialSignature is O(1) and used by the handler's
	// duplicate check; verify both sides.
	require.True(t, k.HasThresholdPartialSignature(ctx, requestID, "p1"))
	require.False(t, k.HasThresholdPartialSignature(ctx, requestID, "p3"))

	// GetSigningStatus rehydrates the slice back to its full shape.
	got, err := k.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	require.Len(t, got.PartialSignatures, 2)
}

// TestGetSigningStatus_MergesLegacyInlineAndSubKeyPartials pins the
// legacy-compat behavior: partial signatures written by a pre-split
// handler (inline on ThresholdSigningRequest) must still be visible, and
// any post-split sub-key entry for the same submitter overrides the
// legacy inline value.
func TestGetSigningStatus_MergesLegacyInlineAndSubKeyPartials(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	requestID := []byte("legacy-req")

	// Write a legacy base struct directly to simulate pre-split state.
	legacy := &types.ThresholdSigningRequest{
		RequestId: requestID,
		Status:    types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
		PartialSignatures: []types.PartialSignature{
			{ParticipantAddress: "a", SlotIndices: []uint32{0, 1}},
			{ParticipantAddress: "b", SlotIndices: []uint32{2}},
		},
	}
	bz, err := k.cdc.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, k.storeService.OpenKVStore(ctx).Set(types.ThresholdSigningRequestKey(requestID), bz))

	// Post-split sub-key write for "b" that overrides the legacy entry.
	require.NoError(t, k.SetThresholdPartialSignature(ctx, requestID, &types.PartialSignature{
		ParticipantAddress: "b",
		SlotIndices:        []uint32{5, 6},
	}))

	got, err := k.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	require.Len(t, got.PartialSignatures, 2)

	var sawA, sawB bool
	for _, ps := range got.PartialSignatures {
		if ps.ParticipantAddress == "a" {
			sawA = true
			require.Equal(t, []uint32{0, 1}, ps.SlotIndices,
				"legacy-only entry must survive rehydration unchanged")
		}
		if ps.ParticipantAddress == "b" {
			sawB = true
			require.Equal(t, []uint32{5, 6}, ps.SlotIndices,
				"sub-key entry must override the legacy inline value")
		}
	}
	require.True(t, sawA)
	require.True(t, sawB)
}

// TestDeleteThresholdPartialSignaturesForRequest_ClearsAllSubKeys exercises
// the retry/reset path: when an expired/failed request is retried, all
// partial-sig sub-keys from the prior attempt must be removed.
func TestDeleteThresholdPartialSignaturesForRequest_ClearsAllSubKeys(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	requestID := []byte("to-reset")
	for _, addr := range []string{"x", "y", "z"} {
		require.NoError(t, k.SetThresholdPartialSignature(ctx, requestID, &types.PartialSignature{
			ParticipantAddress: addr,
		}))
	}
	before, err := k.ListThresholdPartialSignatures(ctx, requestID)
	require.NoError(t, err)
	require.Len(t, before, 3)

	require.NoError(t, k.DeleteThresholdPartialSignaturesForRequest(ctx, requestID))
	after, err := k.ListThresholdPartialSignatures(ctx, requestID)
	require.NoError(t, err)
	require.Empty(t, after)
}
