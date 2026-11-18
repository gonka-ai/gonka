package keeper

import (
	"context"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
	types2 "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetCommitMultiStore(ctx context.Context) {
	sdkContext := types2.UnwrapSDKContext(ctx)
	multiStore := sdkContext.MultiStore()

	commitMultiStore := multiStore.(storetypes.CommitMultiStore)
	k.LogInfo("CommitMultiStore", types.System, "commitMultiStore", commitMultiStore)
}
