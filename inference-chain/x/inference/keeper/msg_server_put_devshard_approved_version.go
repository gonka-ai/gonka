package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) PutDevshardApprovedVersion(goCtx context.Context, msg *types.MsgPutDevshardApprovedVersion) (*types.MsgPutDevshardApprovedVersionResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := msg.Version.Validate(); err != nil {
		return nil, err
	}
	if !k.HasApprovedVersion(ctx, msg.Version.Name) {
		n, err := k.ApprovedVersionCount(ctx)
		if err != nil {
			return nil, err
		}
		if n >= types.MaxDevshardApprovedVersions {
			return nil, types.ErrApprovedVersionsLimit
		}
	}

	if err := k.SetApprovedVersion(ctx, msg.Version); err != nil {
		return nil, err
	}
	return &types.MsgPutDevshardApprovedVersionResponse{}, nil
}
