package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) DeleteDevshardApprovedVersion(goCtx context.Context, msg *types.MsgDeleteDevshardApprovedVersion) (*types.MsgDeleteDevshardApprovedVersionResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if !k.HasApprovedVersion(ctx, msg.Name) {
		return nil, types.ErrApprovedVersionNotFound
	}
	if err := k.DeleteApprovedVersion(ctx, msg.Name); err != nil {
		return nil, err
	}
	return &types.MsgDeleteDevshardApprovedVersionResponse{}, nil
}
