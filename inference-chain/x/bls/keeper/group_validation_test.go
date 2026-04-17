package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// TestGroupKeyValidationState_PrefixIsolation guards against the specific bug
// where GroupValidationPartialSigPrefix shares a prefix with
// GroupValidationPrefix, which would make the genesis-export prefix iterator
// yield partial-sig entries that then fail to unmarshal as
// GroupKeyValidationState.
//
// The fix is enforced by the value of the prefix bytes themselves; this test
// locks that invariant in and will loudly break if either prefix is
// renamed to re-collide.
func TestGroupKeyValidationState_PrefixIsolation(t *testing.T) {
	basePrefix := types.GroupValidationPrefix
	subPrefix := types.GroupValidationPartialSigPrefix

	if len(subPrefix) < len(basePrefix) {
		return
	}
	require.NotEqual(t, string(basePrefix), string(subPrefix[:len(basePrefix)]),
		"GroupValidationPartialSigPrefix must NOT start with GroupValidationPrefix; otherwise a prefix.Store scoped to GroupValidationPrefix would yield partial-sig entries that cannot be decoded as GroupKeyValidationState (corrupt genesis export)")
}

// TestGetGroupKeyValidationState_MigratesLegacyInlinePartials exercises the
// transparent in-flight migration path. A base state is seeded with inline
// PartialSignatures as a pre-split handler would have written it; after
// GetGroupKeyValidationState runs, the inline entries are expected to have
// moved to sub-keys and the base state to have been rewritten with
// PartialSignatures zeroed.
//
// Covers the correctness bug in the first version of the split, where the
// first post-upgrade SetGroupKeyValidationState would discard legacy inline
// entries without syncing them to sub-keys, silently dropping signatures
// and corrupting SlotsCovered on any subsequent submission.
func TestGetGroupKeyValidationState_MigratesLegacyInlinePartials(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const previousEpochID = uint64(1)
	const newEpochID = uint64(2)

	// Seed a previous-epoch Participants list so address→index lookup works.
	prevEpoch := types.EpochBLSData{
		EpochId: previousEpochID,
		Participants: []types.BLSParticipantInfo{
			{Address: "addr-0"},
			{Address: "addr-1"},
			{Address: "addr-2"},
		},
		DealerParts:             []*types.DealerPartStorage{},
		VerificationSubmissions: []*types.VerificationVectorSubmission{},
	}
	require.NoError(t, k.SetEpochBLSData(ctx, prevEpoch))

	// Write a legacy base state directly via the raw KV store: inline
	// PartialSignatures, SlotsCovered reflecting the two inline entries.
	// This mirrors what a pre-split handler would have written.
	legacyBase := &types.GroupKeyValidationState{
		NewEpochId:      newEpochID,
		PreviousEpochId: previousEpochID,
		Status:          types.GroupKeyValidationStatus_GROUP_KEY_VALIDATION_STATUS_COLLECTING_SIGNATURES,
		SlotsCovered:    5,
		MessageHash:     []byte{0x01, 0x02},
		PartialSignatures: []types.PartialSignature{
			{ParticipantAddress: "addr-0", SlotIndices: []uint32{0, 1}, Signature: make([]byte, 96)},
			{ParticipantAddress: "addr-2", SlotIndices: []uint32{10, 11, 12}, Signature: make([]byte, 144)},
		},
	}
	rawStore := k.storeService.OpenKVStore(ctx)
	bz, err := k.cdc.Marshal(legacyBase)
	require.NoError(t, err)
	require.NoError(t, rawStore.Set(types.GroupValidationKey(newEpochID), bz))

	// First read must transparently migrate and return the full set of
	// partials through the sub-key path.
	got, found, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.PartialSignatures, 2, "both legacy partials should have surfaced via sub-keys")

	// Base state after migration must have zero-length PartialSignatures
	// on disk: re-read the raw bytes and check the inline field is empty.
	rawBz, err := rawStore.Get(types.GroupValidationKey(newEpochID))
	require.NoError(t, err)
	var afterMigration types.GroupKeyValidationState
	require.NoError(t, k.cdc.Unmarshal(rawBz, &afterMigration))
	require.Empty(t, afterMigration.PartialSignatures,
		"base state must have zero inline partials after migration")
	require.Equal(t, uint32(5), afterMigration.SlotsCovered,
		"SlotsCovered must be preserved across migration")

	// Sub-keys must now contain the migrated entries at the correct indices.
	ps0, err := k.GetGroupValidationPartialSignature(ctx, newEpochID, 0)
	require.NoError(t, err)
	require.NotNil(t, ps0)
	require.Equal(t, "addr-0", ps0.ParticipantAddress)
	require.Equal(t, []uint32{0, 1}, ps0.SlotIndices)

	ps2, err := k.GetGroupValidationPartialSignature(ctx, newEpochID, 2)
	require.NoError(t, err)
	require.NotNil(t, ps2)
	require.Equal(t, "addr-2", ps2.ParticipantAddress)
	require.Equal(t, []uint32{10, 11, 12}, ps2.SlotIndices)

	// Participant index 1 had no legacy entry; no sub-key expected.
	ps1, err := k.GetGroupValidationPartialSignature(ctx, newEpochID, 1)
	require.NoError(t, err)
	require.Nil(t, ps1)

	// Second read must be a no-op migration: base no longer has inline
	// partials, so we go straight through the sub-key path.
	got2, found2, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	require.True(t, found2)
	require.Len(t, got2.PartialSignatures, 2)
}
