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

// TestGroupKeyValidationState_EagerMigrationViaSet pins the invariant that
// SetGroupKeyValidationState syncs inline PartialSignatures to per-participant
// sub-keys and zeroes the base, so the v0.2.12 eager migration (Walk -> Set)
// converts legacy records without dropping entries.
func TestGroupKeyValidationState_EagerMigrationViaSet(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const previousEpochID = uint64(1)
	const newEpochID = uint64(2)

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

	// Walk -> Set is exactly what migrateGroupKeyValidationStatesToSubKeys
	// runs at upgrade time.
	require.NoError(t, k.WalkGroupKeyValidationStates(ctx, func(state types.GroupKeyValidationState) error {
		if len(state.PartialSignatures) == 0 {
			return nil
		}
		return k.SetGroupKeyValidationState(ctx, &state)
	}))

	rawBz, err := rawStore.Get(types.GroupValidationKey(newEpochID))
	require.NoError(t, err)
	var afterMigration types.GroupKeyValidationState
	require.NoError(t, k.cdc.Unmarshal(rawBz, &afterMigration))
	require.Empty(t, afterMigration.PartialSignatures,
		"base state must have zero inline partials after migration")
	require.Equal(t, uint32(5), afterMigration.SlotsCovered,
		"SlotsCovered must be preserved across migration")

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

	ps1, err := k.GetGroupValidationPartialSignature(ctx, newEpochID, 1)
	require.NoError(t, err)
	require.Nil(t, ps1)

	got, found, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.PartialSignatures, 2,
		"Get must rehydrate migrated partials from sub-keys")

	// Re-running the migration is a no-op: the base now has zero inline
	// partials, so the Walk callback short-circuits.
	require.NoError(t, k.WalkGroupKeyValidationStates(ctx, func(state types.GroupKeyValidationState) error {
		require.Empty(t, state.PartialSignatures,
			"idempotent re-run must observe no inline partials")
		return nil
	}))
}
