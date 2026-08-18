package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SetTrainingNodeOptIn(goCtx context.Context, msg *types.MsgSetTrainingNodeOptIn) (*types.MsgSetTrainingNodeOptInResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	if msg.NodeId == "" {
		return nil, types.ErrPocNodeIdEmpty
	}

	if msg.OptIn {
		hardware, found := k.GetHardwareNodes(goCtx, msg.Creator)
		if !found || !hasHardwareNode(hardware, msg.NodeId) {
			return nil, types.ErrTrainshardNodeNotOwned.Wrapf("node %s not owned by %s", msg.NodeId, msg.Creator)
		}
		if _, err := k.setTrainingOptIn(goCtx, msg.Creator, msg.NodeId, sdk.UnwrapSDKContext(goCtx).BlockHeight()); err != nil {
			return nil, err
		}
		return &types.MsgSetTrainingNodeOptInResponse{}, nil
	}

	if k.IsNodeActivelyReserved(goCtx, msg.Creator, msg.NodeId) {
		return nil, types.ErrTrainshardNodeReserved
	}
	if err := k.TrainingNodeOptIns.Remove(goCtx, collections.Join(msg.Creator, msg.NodeId)); err != nil {
		return nil, err
	}
	return &types.MsgSetTrainingNodeOptInResponse{}, nil
}

func hasHardwareNode(nodes *types.HardwareNodes, nodeId string) bool {
	if nodes == nil {
		return false
	}
	for _, n := range nodes.HardwareNodes {
		if n.GetLocalId() == nodeId {
			return true
		}
	}
	return false
}
