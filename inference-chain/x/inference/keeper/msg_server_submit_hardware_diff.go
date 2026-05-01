package keeper

import (
	"context"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
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

	// Skip the write when the resulting node set is operationally identical
	// to what's already stored. Catches the chatty-DAPI case where the same
	// diff fires every block. NOT a security control — a malicious sender
	// can defeat this by flipping any compared field. The point is to stop
	// honest no-op spam, not to rate-limit.
	if HardwareNodesUnchanged(updatedNodes.HardwareNodes, existingNodes.HardwareNodes) {
		k.LogDebug("SubmitHardwareDiff no-op: skipping write", types.Nodes,
			"participant", msg.Creator, "nodeCount", len(updatedNodes.HardwareNodes))
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
// equivalent in their *operational* fields: local_id, status, models,
// hardware, host, port. The Version field is excluded because it's
// explicitly informational (see hardware_node.proto) and a flapping value
// shouldn't trigger spurious writes.
//
// This is a best-effort idempotency check, not a security boundary. A
// caller that wants to bypass it can flip any operational field.
// Exported for testing.
func HardwareNodesUnchanged(after, before []*types.HardwareNode) bool {
	if len(after) != len(before) {
		return false
	}
	for i := range after {
		if !hardwareNodeOperationalEqual(after[i], before[i]) {
			return false
		}
	}
	return true
}

// hardwareNodeOperationalEqual compares the load-bearing fields of two
// HardwareNode protos. Add to this list if hardware_node.proto adds new
// state-affecting fields.
func hardwareNodeOperationalEqual(a, b *types.HardwareNode) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.LocalId != b.LocalId ||
		a.Status != b.Status ||
		a.Host != b.Host ||
		a.Port != b.Port {
		return false
	}
	if !slices.Equal(a.Models, b.Models) {
		return false
	}
	if len(a.Hardware) != len(b.Hardware) {
		return false
	}
	for i := range a.Hardware {
		if a.Hardware[i].Type != b.Hardware[i].Type ||
			a.Hardware[i].Count != b.Hardware[i].Count {
			return false
		}
	}
	return true
}
