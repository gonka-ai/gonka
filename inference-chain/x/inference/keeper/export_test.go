package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// GetMustBeValidatedInferencesForTesting exposes the unexported handler method
// for direct unit testing of validation-sampling overflow behaviour.
func GetMustBeValidatedInferencesForTesting(ms types.MsgServer, ctx sdk.Context, msg *types.MsgClaimRewards) ([]string, error) {
	return ms.(*msgServer).getMustBeValidatedInferences(ctx, msg)
}

func CheckPoCV2StoreCommitRecheckOverlapForTesting(k Keeper, ctx sdk.Context, msg *types.MsgPoCV2StoreCommit) error {
	return k.checkPoCV2StoreCommitRecheckOverlap(ctx, msg)
}

func SetPoCV2StoreCommitRawBytesForTesting(k Keeper, ctx sdk.Context, startHeight int64, addr sdk.AccAddress, modelID string, bz []byte) error {
	pk := pocV2StoreCommitKey(startHeight, addr, modelID)
	keyBz, err := collections.EncodeKeyWithPrefix(
		types.PoCV2StoreCommitPrefix,
		collections.TripleKeyCodec(collections.Int64Key, sdk.AccAddressKey, collections.StringKey),
		pk,
	)
	if err != nil {
		return err
	}
	return k.storeService.OpenKVStore(ctx).Set(keyBz, bz)
}

func CollectModelFreeWeightsForTesting(k Keeper, ctx sdk.Context, epochId uint64, modelId string) map[string]int64 {
	return k.collectModelFreeWeights(ctx, epochId, modelId)
}

// SetConfirmationCleanupFailAfterForTest fails the Nth confirmation-prefix
// removal. The hook clears itself when it fires.
func SetConfirmationCleanupFailAfterForTest(n int) {
	confirmationCleanupFailAfter = n
}

func (k Keeper) RemoveFromEpochGroupsForTesting(ctx sdk.Context, participant *types.Participant, reason calculations.ParticipantStatusReason) error {
	return k.removeFromEpochGroups(ctx, participant, reason)
}

func SetRawStoreBytesForTesting(k Keeper, ctx sdk.Context, key, value []byte) error {
	return k.storeService.OpenKVStore(ctx).Set(key, value)
}

func HasRawStoreKeyForTesting(k Keeper, ctx sdk.Context, key []byte) (bool, error) {
	return k.storeService.OpenKVStore(ctx).Has(key)
}

// PlantActiveParticipantsForTest writes an active-participant record without
// the cache address check, so a test can store a legacy invalid index.
func PlantActiveParticipantsForTest(k Keeper, ctx context.Context, record types.ActiveParticipants) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})
	bz, err := k.cdc.Marshal(&record)
	if err != nil {
		return err
	}
	store.Set(types.ActiveParticipantsFullKey(record.EpochId), bz)
	return nil
}
