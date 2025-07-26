package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) CreatePartialUpgrade(goCtx context.Context, msg *types.MsgCreatePartialUpgrade) (*types.MsgCreatePartialUpgradeResponse, error) {
	if k.GetAuthority() != msg.Authority {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.GetAuthority(), msg.Authority)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("CreatePartialUpgrade", types.Upgrades, "height", msg.Height, "node_version", msg.NodeVersion, "api_binaries_json", msg.ApiBinariesJson)
	k.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Height:          msg.Height,
		NodeVersion:     msg.NodeVersion,
		ApiBinariesJson: msg.ApiBinariesJson,
		Name:            "PartialUpgrade at height " + strconv.FormatUint(msg.Height, 10) + " for node version " + msg.NodeVersion,
	})

	return &types.MsgCreatePartialUpgradeResponse{}, nil
}
