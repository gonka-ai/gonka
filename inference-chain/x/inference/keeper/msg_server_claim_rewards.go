package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	
)

func (k msgServer) ClaimRewards(goCtx context.Context, msg *types.MsgClaimRewards) (*types.MsgClaimRewardsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// TODO: Handling the message
	_ = ctx

	return &types.MsgClaimRewardsResponse{}, nil
}
