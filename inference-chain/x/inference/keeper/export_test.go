package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// GetMustBeValidatedInferencesForTesting exposes the unexported handler method
// for direct unit testing of validation-sampling overflow behaviour.
func GetMustBeValidatedInferencesForTesting(ms types.MsgServer, ctx sdk.Context, msg *types.MsgClaimRewards) ([]string, error) {
	return ms.(*msgServer).getMustBeValidatedInferences(ctx, msg)
}
