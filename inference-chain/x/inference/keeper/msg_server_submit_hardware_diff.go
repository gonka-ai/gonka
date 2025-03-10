package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitHardwareDiff(goCtx context.Context, msg *types.MsgSubmitHardwareDiff) (*types.MsgSubmitHardwareDiffResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	existingNodes, found := k.GetHardwareNodes(ctx, msg.Creator)
	if !found {
		existingNodes = &types.HardwareNodes{
			HardwareNodes: []*types.HardwareNode{},
		}
	}

	nodeMap := make(map[string]*types.HardwareNode)
	for _, node := range existingNodes.HardwareNodes {
		nodeMap[node.LocalId] = node
	}

	for _, nodeToRemove := range msg.Removed {
		delete(nodeMap, nodeToRemove.LocalId)
	}

	for _, node := range msg.NewOrModified {
		nodeMap[node.LocalId] = node
	}

	updatedNodes := &types.HardwareNodes{
		Participant:   msg.Creator,
		HardwareNodes: make([]*types.HardwareNode, 0, len(nodeMap)),
	}
	for _, node := range nodeMap {
		updatedNodes.HardwareNodes = append(updatedNodes.HardwareNodes, node)
	}

	if err := k.SetHardwareNodes(ctx, updatedNodes); err != nil {
		k.LogError("Error setting hardware nodes", types.Nodes, "err", err)
		return nil, err
	}

	return &types.MsgSubmitHardwareDiffResponse{}, nil
}
