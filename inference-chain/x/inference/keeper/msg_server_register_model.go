package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterModel(goCtx context.Context, msg *types.MsgRegisterModel) (*types.MsgRegisterModelResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.SetModel(ctx, &types.Model{
		SubmittedBy:           msg.Creator,
		Id:                    msg.Id,
		UnitOfComputePerToken: msg.UnitOfComputePerToken,
	})

	return &types.MsgRegisterModelResponse{}, nil
}
