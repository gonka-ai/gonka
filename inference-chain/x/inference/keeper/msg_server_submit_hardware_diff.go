package keeper

import (
	"context"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/exp/slices"
)

func (k msgServer) SubmitHardwareDiff(goCtx context.Context, msg *types.MsgSubmitHardwareDiff) (*types.MsgSubmitHardwareDiffResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Check for duplicate LocalIds
	seenIds := make(map[string]bool)
	for _, node := range msg.NewOrModified {
		if seenIds[node.LocalId] {
			return nil, types.ErrDuplicateNodeId
		}
		seenIds[node.LocalId] = true
	}
	for _, node := range msg.Removed {
		if seenIds[node.LocalId] {
			return nil, types.ErrDuplicateNodeId
		}
		seenIds[node.LocalId] = true
	}

	// Make sure that before the update, we have models in the state
	for _, node := range msg.NewOrModified {

		for _, modelId := range node.Models {
			if !k.IsValidGovernanceModel(ctx, modelId) {
				return nil, types.ErrInvalidModel
			}
		}
	}

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
	slices.SortFunc(updatedNodes.HardwareNodes, func(a, b *types.HardwareNode) int {
		return strings.Compare(a.LocalId, b.LocalId)
	})

	// Early no-op: skip the write when the resulting node set is identical
	// to what's already stored. Without this guard, a chatty DAPI sending
	// the same diff every block would consume block space and (when fees
	// re-enable) the host's gas-bypass budget for zero state change.
	// Both lists are sorted by LocalId — `updatedNodes` was just sorted
	// above, and `existingNodes` was sorted at its last write — so a
	// length + pairwise proto.Equal compare is sufficient.
	if HardwareNodesUnchanged(updatedNodes.HardwareNodes, existingNodes.HardwareNodes) {
		k.LogDebug("SubmitHardwareDiff no-op: posted state matches stored state, skipping write",
			types.Nodes, "participant", msg.Creator, "nodeCount", len(updatedNodes.HardwareNodes))
		return &types.MsgSubmitHardwareDiffResponse{}, nil
	}

	k.LogInfo("Updating hardware nodes", types.Nodes, "nodes", updatedNodes)
	if err := k.SetHardwareNodes(ctx, updatedNodes); err != nil {
		k.LogError("Error setting hardware nodes", types.Nodes, "err", err)
		return nil, err
	}

	return &types.MsgSubmitHardwareDiffResponse{}, nil
}

// HardwareNodesUnchanged reports whether two sorted hardware-node lists are
// element-wise identical. Used as a cheap idempotency check on
// MsgSubmitHardwareDiff so DAPI's per-block diff submissions don't trigger
// state writes when nothing actually changed. Exported for testing.
func HardwareNodesUnchanged(after, before []*types.HardwareNode) bool {
	if len(after) != len(before) {
		return false
	}
	for i := range after {
		if !proto.Equal(after[i], before[i]) {
			return false
		}
	}
	return true
}
