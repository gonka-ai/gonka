package keeper

import (
	"context"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterModel(goCtx context.Context, msg *types.MsgRegisterModel) (*types.MsgRegisterModelResponse, error) {
	if k.GetAuthority() != msg.Creator {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "MsgRegisterModel. invalid authority/creator; expected %s, got %s", k.GetAuthority(), msg.Creator)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	k.SetModel(ctx, &types.Model{
		SubmittedBy:            msg.Creator,
		Id:                     msg.Id,
		UnitsOfComputePerToken: msg.UnitsOfComputePerToken,
	})

	return &types.MsgRegisterModelResponse{}, nil
}
