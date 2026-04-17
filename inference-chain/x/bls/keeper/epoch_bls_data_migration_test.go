package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// TestSetEpochBLSData_StripsAndSyncsVerificationSubmissions exercises the
// invariant that SetEpochBLSData must (a) persist VerificationSubmissions
// entries to per-participant sub-keys and (b) zero the inline slice in the
// base struct so later writes stay constant-size. This is the fix for the
// N^2 WritePerByte growth the verifier phase otherwise hits, mirroring the
// earlier dealer-part split.
func TestSetEpochBLSData_StripsAndSyncsVerificationSubmissions(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const epochID = uint64(7)

	// Seed a base struct with a non-empty verification submission for one
	// participant (the shape a pre-split handler would have produced).
	epochData := types.EpochBLSData{
		EpochId: epochID,
		Participants: []types.BLSParticipantInfo{
			{Address: "addr-0"},
			{Address: "addr-1"},
			{Address: "addr-2"},
		},
		DealerParts: []*types.DealerPartStorage{},
		VerificationSubmissions: []*types.VerificationVectorSubmission{
			{DealerValidity: []bool{true, true, true}},
			{DealerValidity: []bool{}},
			{DealerValidity: []bool{true, false, true}},
		},
	}
	require.NoError(t, k.SetEpochBLSData(ctx, epochData))

	// The on-disk base must NOT carry inline verification submissions.
	rawStore := k.storeService.OpenKVStore(ctx)
	rawBz, err := rawStore.Get(types.EpochBLSDataKey(epochID))
	require.NoError(t, err)
	require.NotNil(t, rawBz)
	var persisted types.EpochBLSData
	require.NoError(t, k.cdc.Unmarshal(rawBz, &persisted))
	require.Empty(t, persisted.VerificationSubmissions,
		"base struct must have zero inline verification submissions; otherwise the N^2 write-per-byte bug returns")

	// Non-empty submissions should have moved to sub-keys at the right indices.
	vs0, err := k.GetVerificationSubmission(ctx, epochID, 0)
	require.NoError(t, err)
	require.NotNil(t, vs0)
	require.Equal(t, []bool{true, true, true}, vs0.DealerValidity)

	vs2, err := k.GetVerificationSubmission(ctx, epochID, 2)
	require.NoError(t, err)
	require.NotNil(t, vs2)
	require.Equal(t, []bool{true, false, true}, vs2.DealerValidity)

	// Empty placeholder at index 1 should NOT have a sub-key — we don't
	// want sentinels cluttering storage.
	vs1, err := k.GetVerificationSubmission(ctx, epochID, 1)
	require.NoError(t, err)
	require.Nil(t, vs1)

	// GetEpochBLSData should rehydrate the slice back to its full shape,
	// transparently to callers.
	got, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, got.VerificationSubmissions, 3)
	require.Equal(t, []bool{true, true, true}, got.VerificationSubmissions[0].DealerValidity)
	require.Empty(t, got.VerificationSubmissions[1].DealerValidity,
		"slot 1 must come back as the empty placeholder since no sub-key exists")
	require.Equal(t, []bool{true, false, true}, got.VerificationSubmissions[2].DealerValidity)
}

// TestGetEpochBLSData_MergesLegacyInlineAndSubKeyVerificationSubmissions
// pins the legacy-compatibility behavior: an EpochBLSData written by a
// pre-split handler (inline VerificationSubmissions) must continue to work
// after upgrade, with any post-upgrade sub-key entries taking precedence.
func TestGetEpochBLSData_MergesLegacyInlineAndSubKeyVerificationSubmissions(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const epochID = uint64(9)

	// Write a legacy base struct directly, bypassing SetEpochBLSData, so
	// the inline VerificationSubmissions are persisted as a pre-split
	// handler would have produced them.
	legacy := &types.EpochBLSData{
		EpochId: epochID,
		Participants: []types.BLSParticipantInfo{
			{Address: "addr-0"},
			{Address: "addr-1"},
		},
		DealerParts: []*types.DealerPartStorage{},
		VerificationSubmissions: []*types.VerificationVectorSubmission{
			{DealerValidity: []bool{true, true}},
			{DealerValidity: []bool{false, true}},
		},
	}
	bz, err := k.cdc.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, k.storeService.OpenKVStore(ctx).Set(types.EpochBLSDataKey(epochID), bz))

	// A post-upgrade write for participant 1 lands in the sub-key. The
	// legacy inline entry for 1 must be overridden; the legacy entry for
	// 0 must still be visible via the rehydrated slice.
	require.NoError(t, k.SetVerificationSubmission(ctx, epochID, 1, &types.VerificationVectorSubmission{
		DealerValidity: []bool{true, false},
	}))

	got, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, got.VerificationSubmissions, 2)
	require.Equal(t, []bool{true, true}, got.VerificationSubmissions[0].DealerValidity,
		"legacy inline entry at index 0 must survive rehydration")
	require.Equal(t, []bool{true, false}, got.VerificationSubmissions[1].DealerValidity,
		"sub-key entry at index 1 must override the legacy inline entry")
}
