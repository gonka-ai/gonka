package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) AddParticipantsToAllowList(goCtx context.Context, msg *types.MsgAddParticipantsToAllowList) (*types.MsgAddParticipantsToAllowListResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	for _, a := range msg.Addresses {
		addr, err := sdk.AccAddressFromBech32(a)
		if err != nil {
			return nil, err
		}
		if err := k.ParticipantAllowListSet.Set(ctx, addr); err != nil {
			return nil, err
		}
	}

	k.LogInfo("Added participants to allow list", types.Participants, "count", len(msg.Addresses))

	return &types.MsgAddParticipantsToAllowListResponse{}, nil
}
