package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) DeleteGovernanceModel(goCtx context.Context, msg *types.MsgDeleteGovernanceModel) (*types.MsgDeleteGovernanceModelResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.GetAuthority(), msg.Authority)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if _, found := k.GetGovernanceModel(ctx, msg.Id); !found {
		return nil, types.ErrInvalidModel
	}

	k.Keeper.DeleteGovernanceModel(ctx, msg.Id)
	return &types.MsgDeleteGovernanceModelResponse{}, nil
}
