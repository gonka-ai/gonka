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

// TestSetGroupKeyValidationState_SyncsInlinePartials exercises the sync
// path used by the v0.2.12 upgrade migration and genesis import:
// SetGroupKeyValidationState, called with inline PartialSignatures, must
// write them to per-participant sub-keys (resolving addr→index from the
// previous epoch's Participants) and persist the base with
// PartialSignatures zeroed.
//
// Guards the correctness bug where the first version of the split would
// discard legacy inline entries without syncing them, silently dropping
// signatures and corrupting SlotsCovered on any subsequent submission.
func TestSetGroupKeyValidationState_SyncsInlinePartials(t *testing.T) {
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

	// Call SetGroupKeyValidationState with inline PartialSignatures — the
	// same shape the upgrade migration passes when it rehydrates legacy
	// base state via WalkGroupKeyValidationStates.
	withInline := &types.GroupKeyValidationState{
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
	require.NoError(t, k.SetGroupKeyValidationState(ctx, withInline))

	// GetGroupKeyValidationState returns the full set of partials through
	// the sub-key path.
	got, found, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got.PartialSignatures, 2, "both inline partials should have surfaced via sub-keys")

	// Base state on disk must have zero-length PartialSignatures: re-read
	// the raw bytes and check the inline field is empty.
	rawStore := k.storeService.OpenKVStore(ctx)
	rawBz, err := rawStore.Get(types.GroupValidationKey(newEpochID))
	require.NoError(t, err)
	var afterSync types.GroupKeyValidationState
	require.NoError(t, k.cdc.Unmarshal(rawBz, &afterSync))
	require.Empty(t, afterSync.PartialSignatures,
		"base state must have zero inline partials after sync")
	require.Equal(t, uint32(5), afterSync.SlotsCovered,
		"SlotsCovered must be preserved across sync")

	// Sub-keys must now contain the synced entries at the correct indices.
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

	// Participant index 1 had no inline entry; no sub-key expected.
	ps1, err := k.GetGroupValidationPartialSignature(ctx, newEpochID, 1)
	require.NoError(t, err)
	require.Nil(t, ps1)

	// Second Set with PartialSignatures == nil (runtime hot path) must
	// leave the sub-keys untouched: the base is still persisted with
	// zero inline partials, and GetGroupKeyValidationState still returns
	// the two previously-synced partials via the sub-key path.
	hotPath := *got
	hotPath.PartialSignatures = nil
	require.NoError(t, k.SetGroupKeyValidationState(ctx, &hotPath))

	got2, found2, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	require.True(t, found2)
	require.Len(t, got2.PartialSignatures, 2)
}
