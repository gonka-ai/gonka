package keeper

import (
	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
