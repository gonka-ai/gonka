package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RemoveUserFromTrainingAllowList(goCtx context.Context, msg *types.MsgRemoveUserFromTrainingAllowList) (*types.MsgRemoveUserFromTrainingAllowListResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	addr, err := sdk.AccAddressFromBech32(msg.Address)
	if err != nil {
		return nil, err
	}
	switch msg.Role {
	case types.TrainingRole_ROLE_EXEC:
		if err := k.TrainingExecAllowListSet.Remove(ctx, addr); err != nil {
			return nil, err
		}
	case types.TrainingRole_ROLE_START:
		if err := k.TrainingStartAllowListSet.Remove(ctx, addr); err != nil {
			return nil, err
		}
	}
	k.LogInfo("Removed user from training allow list", types.Training, "address", addr, "role", msg.Role)

	return &types.MsgRemoveUserFromTrainingAllowListResponse{}, nil
}
