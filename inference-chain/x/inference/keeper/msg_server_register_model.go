package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterModel(goCtx context.Context, msg *types.MsgRegisterModel) (*types.MsgRegisterModelResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "MsgRegisterModel. invalid authority; expected %s, got %s", k.GetAuthority(), msg.Authority)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	k.SetModel(ctx, &types.Model{
		ProposedBy:             msg.ProposedBy,
		Id:                     msg.Id,
		UnitsOfComputePerToken: msg.UnitsOfComputePerToken,
		HfRepo:                 msg.HfRepo,
		HfCommit:               msg.HfCommit,
		ModelArgs:              msg.ModelArgs,
		VRam:                   msg.VRam,
		ThroughputPerNonce:     msg.ThroughputPerNonce,
		ValidationThreshold:    msg.ValidationThreshold,
	})

	return &types.MsgRegisterModelResponse{}, nil
}
