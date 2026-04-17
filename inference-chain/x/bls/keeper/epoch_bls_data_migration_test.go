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

// TestSetEpochBLSData_StripsAndSyncsDealerComplaints pins the same
// strip-and-sync invariant for DealerComplaints. The verifier handler
// appended complaints to an inline slice pre-split, causing O(N^2) gas
// growth; the split moves them to per-(dealer, complainer) sub-keys.
func TestSetEpochBLSData_StripsAndSyncsDealerComplaints(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const epochID = uint64(11)

	epochData := types.EpochBLSData{
		EpochId: epochID,
		Participants: []types.BLSParticipantInfo{
			{Address: "addr-0"},
			{Address: "addr-1"},
			{Address: "addr-2"},
		},
		DealerComplaints: []types.DealerComplaint{
			{DealerIndex: 0, ComplainerIndex: 1, DisputedSlotIndex: 3, DisputedCiphertextIndex: 7},
			{DealerIndex: 2, ComplainerIndex: 0, DisputedSlotIndex: 11, DisputedCiphertextIndex: 22, ResponseSubmitted: true},
		},
	}
	require.NoError(t, k.SetEpochBLSData(ctx, epochData))

	rawStore := k.storeService.OpenKVStore(ctx)
	rawBz, err := rawStore.Get(types.EpochBLSDataKey(epochID))
	require.NoError(t, err)
	require.NotNil(t, rawBz)
	var persisted types.EpochBLSData
	require.NoError(t, k.cdc.Unmarshal(rawBz, &persisted))
	require.Empty(t, persisted.DealerComplaints,
		"base struct must have zero inline dealer complaints; otherwise the N^2 write-per-byte bug returns")

	c01, err := k.GetDealerComplaint(ctx, epochID, 0, 1)
	require.NoError(t, err)
	require.NotNil(t, c01)
	require.Equal(t, uint32(3), c01.DisputedSlotIndex)
	require.Equal(t, uint32(7), c01.DisputedCiphertextIndex)

	c20, err := k.GetDealerComplaint(ctx, epochID, 2, 0)
	require.NoError(t, err)
	require.NotNil(t, c20)
	require.True(t, c20.ResponseSubmitted, "dispute-phase mutations must round-trip through sub-key storage")

	// No complaint for (dealer 1, complainer 2).
	require.False(t, k.HasDealerComplaint(ctx, epochID, 1, 2))

	// GetEpochBLSData rehydrates the slice; order is ascending by
	// (dealerIdx, complainerIdx) — (0,1) before (2,0).
	got, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, got.DealerComplaints, 2)
	require.Equal(t, uint32(0), got.DealerComplaints[0].DealerIndex)
	require.Equal(t, uint32(1), got.DealerComplaints[0].ComplainerIndex)
	require.Equal(t, uint32(2), got.DealerComplaints[1].DealerIndex)
	require.Equal(t, uint32(0), got.DealerComplaints[1].ComplainerIndex)
}

// TestDeleteDealerComplaintsForEpoch_ClearsAllSubKeys exercises the path
// used by phase transitions when filtering out complaints against
// non-candidate dealers. The filter needs the sub-keys gone, otherwise
// stale complaints would leak into the next phase.
func TestDeleteDealerComplaintsForEpoch_ClearsAllSubKeys(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const epochID = uint64(13)

	complaints := []types.DealerComplaint{
		{DealerIndex: 0, ComplainerIndex: 1},
		{DealerIndex: 0, ComplainerIndex: 2},
		{DealerIndex: 3, ComplainerIndex: 4},
	}
	for i := range complaints {
		require.NoError(t, k.SetDealerComplaint(ctx, epochID, &complaints[i]))
	}
	before, err := k.ListDealerComplaintsForEpoch(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, before, 3)

	require.NoError(t, k.DeleteDealerComplaintsForEpoch(ctx, epochID))
	after, err := k.ListDealerComplaintsForEpoch(ctx, epochID)
	require.NoError(t, err)
	require.Empty(t, after)
}

// TestGetEpochBLSData_MergesLegacyInlineAndSubKeyDealerComplaints pins the
// legacy-compat behavior for complaints: legacy inline complaints remain
// visible after upgrade, and any sub-key entry for the same (dealer,
// complainer) pair overrides the inline value. Critical for the dispute
// flow — we can't silently lose a complaint just because it was written
// before the split, and we can't serve a stale complaint after a
// post-upgrade SetDealerComplaint updates it.
func TestGetEpochBLSData_MergesLegacyInlineAndSubKeyDealerComplaints(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const epochID = uint64(15)

	// Pre-split-handler shape: base struct with inline DealerComplaints,
	// no sub-keys.
	legacy := &types.EpochBLSData{
		EpochId: epochID,
		Participants: []types.BLSParticipantInfo{
			{Address: "addr-0"}, {Address: "addr-1"}, {Address: "addr-2"},
		},
		DealerComplaints: []types.DealerComplaint{
			{DealerIndex: 0, ComplainerIndex: 1, DisputedSlotIndex: 5},
			{DealerIndex: 2, ComplainerIndex: 0, DisputedSlotIndex: 9},
		},
	}
	bz, err := k.cdc.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, k.storeService.OpenKVStore(ctx).Set(types.EpochBLSDataKey(epochID), bz))

	// Post-split-handler writes an updated complaint for (0,1) via the
	// sub-key path — e.g. the dispute handler mutating ResponseSubmitted.
	require.NoError(t, k.SetDealerComplaint(ctx, epochID, &types.DealerComplaint{
		DealerIndex:       0,
		ComplainerIndex:   1,
		DisputedSlotIndex: 5,
		ResponseSubmitted: true,
	}))

	got, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, got.DealerComplaints, 2,
		"the legacy-only (2,0) complaint must still be visible alongside the sub-key-overridden (0,1)")

	// Find the (0,1) entry — sub-key override should have won.
	var saw01, saw20 bool
	for _, c := range got.DealerComplaints {
		if c.DealerIndex == 0 && c.ComplainerIndex == 1 {
			saw01 = true
			require.True(t, c.ResponseSubmitted, "sub-key entry must override the legacy inline value")
		}
		if c.DealerIndex == 2 && c.ComplainerIndex == 0 {
			saw20 = true
			require.False(t, c.ResponseSubmitted, "legacy-only complaint must survive rehydration unchanged")
		}
	}
	require.True(t, saw01)
	require.True(t, saw20)
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
