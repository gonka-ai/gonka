package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterModel(goCtx context.Context, msg *types.MsgRegisterModel) (*types.MsgRegisterModelResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.SetModel(ctx, &types.Model{
		SubmittedBy:            msg.Creator,
		Id:                     msg.Id,
		UnitsOfComputePerToken: msg.UnitsOfComputePerToken,
	})

	return &types.MsgRegisterModelResponse{}, nil
}
