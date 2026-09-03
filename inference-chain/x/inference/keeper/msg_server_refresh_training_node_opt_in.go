package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RefreshTrainingNodeOptIn(goCtx context.Context, msg *types.MsgRefreshTrainingNodeOptIn) (*types.MsgRefreshTrainingNodeOptInResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	if len(msg.NodeIds) == 0 || len(msg.NodeIds) > types.MaxRefreshOptInNodes {
		return nil, types.ErrTrainshardOptInRequest.Wrapf("node_ids count must be in (0, %d]", types.MaxRefreshOptInNodes)
	}

	hardware, found := k.GetHardwareNodes(goCtx, msg.Creator)
	if !found {
		return nil, types.ErrTrainshardNodeNotOwned.Wrapf("no hardware nodes for %s", msg.Creator)
	}

	height := sdk.UnwrapSDKContext(goCtx).BlockHeight()
	var expiresAt int64
	for _, nodeId := range msg.NodeIds {
		if !hasHardwareNode(hardware, nodeId) {
			return nil, types.ErrTrainshardNodeNotOwned.Wrapf("node %s not owned by %s", nodeId, msg.Creator)
		}
		var err error
		if expiresAt, err = k.setTrainingOptIn(goCtx, msg.Creator, nodeId, height); err != nil {
			return nil, err
		}
	}

	return &types.MsgRefreshTrainingNodeOptInResponse{ExpiresAtHeight: expiresAt}, nil
}
