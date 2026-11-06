package keeper

import (
	"context"

	"cosmossdk.io/collections"
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

// PruneEpochZeroInferencesForTesting runs the epoch-0 pass alone with remove in place of
// Inferences.Remove.
func PruneEpochZeroInferencesForTesting(k Keeper, ctx sdk.Context, currentEpochIndex int64, remove func(ctx context.Context, id string) error) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	p := k.GetEpochZeroInferencePruner(params)
	p.remove = remove
	return p.prune(ctx, k, currentEpochIndex, nil)
}

func (k Keeper) RemoveFromEpochGroupsForTesting(ctx sdk.Context, participant *types.Participant, reason calculations.ParticipantStatusReason) error {
	return k.removeFromEpochGroups(ctx, participant, reason)
}
