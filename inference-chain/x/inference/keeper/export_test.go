package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func GetMustBeValidatedInferencesForTesting(ms types.MsgServer, ctx sdk.Context, msg *types.MsgClaimRewards) ([]string, error) {
	return ms.(*msgServer).getMustBeValidatedInferences(ctx, msg)
}
