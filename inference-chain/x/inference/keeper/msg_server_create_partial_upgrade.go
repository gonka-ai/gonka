package keeper

import (
	"context"
	"strconv"

	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) CreatePartialUpgrade(goCtx context.Context, msg *types.MsgCreatePartialUpgrade) (*types.MsgCreatePartialUpgradeResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("CreatePartialUpgrade", types.Upgrades, "height", msg.Height, "node_version", msg.NodeVersion, "api_binaries_json", msg.ApiBinariesJson)
	err := k.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Height:          msg.Height,
		NodeVersion:     msg.NodeVersion,
		ApiBinariesJson: msg.ApiBinariesJson,
		Name:            "PartialUpgrade at height " + strconv.FormatUint(msg.Height, 10) + " for node version " + msg.NodeVersion,
	})

	if err != nil {
		return nil, err
	}

	return &types.MsgCreatePartialUpgradeResponse{}, nil
}
