package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SetTrainingNodeOptIn(goCtx context.Context, msg *types.MsgSetTrainingNodeOptIn) (*types.MsgSetTrainingNodeOptInResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	if msg.NodeId == "" {
		return nil, types.ErrPocNodeIdEmpty
	}

	key := collections.Join(msg.Creator, msg.NodeId)

	if msg.OptIn {
		hardware, found := k.GetHardwareNodes(goCtx, msg.Creator)
		if !found || !hasHardwareNode(hardware, msg.NodeId) {
			return nil, types.ErrTrainshardNodeNotOwned.Wrapf("node %s not owned by %s", msg.NodeId, msg.Creator)
		}
		if err := k.TrainingNodeOptIns.Set(goCtx, key); err != nil {
			return nil, err
		}
		return &types.MsgSetTrainingNodeOptInResponse{}, nil
	}

	if k.IsNodeReserved(goCtx, msg.Creator, msg.NodeId) {
		return nil, types.ErrTrainshardNodeReserved
	}
	if err := k.TrainingNodeOptIns.Remove(goCtx, key); err != nil {
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
