package keeper

import (
	"context"

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
		return nil, types.ErrTrainshardOptInRequest.Wrap("no manual opt-in: trainshardd opts the node in once its checks pass")
	}

	if k.IsNodeActivelyReserved(goCtx, msg.Creator, msg.NodeId) {
		return nil, types.ErrTrainshardNodeReserved
	}
	if err := k.clearTrainingOptIn(goCtx, msg.Creator, msg.NodeId); err != nil {
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
