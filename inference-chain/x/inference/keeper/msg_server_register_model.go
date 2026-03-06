package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterModel(goCtx context.Context, msg *types.MsgRegisterModel) (*types.MsgRegisterModelResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
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
