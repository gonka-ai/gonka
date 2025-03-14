package keeper

import (
	"context"
	errorsmod "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) CreatePartialUpgrade(goCtx context.Context, msg *types.MsgCreatePartialUpgrade) (*types.MsgCreatePartialUpgradeResponse, error) {
	if k.GetAuthority() != msg.Creator {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.GetAuthority(), msg.Creator)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Height:          msg.Height,
		NodeVersion:     msg.NodeVersion,
		ApiBinariesJson: msg.ApiBinariesJson,
	})

	return &types.MsgCreatePartialUpgradeResponse{}, nil
}
