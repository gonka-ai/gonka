package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RemoveParticipantsFromAllowList(goCtx context.Context, msg *types.MsgRemoveParticipantsFromAllowList) (*types.MsgRemoveParticipantsFromAllowListResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.GetAuthority(), msg.Authority)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	for _, a := range msg.Addresses {
		addr, err := sdk.AccAddressFromBech32(a)
		if err != nil {
			return nil, err
		}
		if err := k.ParticipantAllowListSet.Remove(ctx, addr); err != nil {
			return nil, err
		}
	}

	k.LogInfo("Removed participants from allow list", types.Participants, "count", len(msg.Addresses))

	return &types.MsgRemoveParticipantsFromAllowListResponse{}, nil
}
