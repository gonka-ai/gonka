package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) DeleteGovernanceModel(goCtx context.Context, msg *types.MsgDeleteGovernanceModel) (*types.MsgDeleteGovernanceModelResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if _, found := k.GetGovernanceModel(ctx, msg.Id); !found {
		return nil, types.ErrInvalidModel
	}

	k.Keeper.DeleteGovernanceModel(ctx, msg.Id)
	return &types.MsgDeleteGovernanceModelResponse{}, nil
}
