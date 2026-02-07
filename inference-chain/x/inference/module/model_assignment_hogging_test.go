package inference

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestInferenceSlotRotation_FixVerification(t *testing.T) {
	// Setup: Participant with 2 nodes
	// NodeA: weight 10 (Smallest)
	// NodeB: weight 20
	
	nodeA := &types.MLNodeInfo{
		NodeId:             "NodeA_Small",
		PocWeight:          10,
		TimeslotAllocation: []bool{true, false},
	}
	nodeB := &types.MLNodeInfo{
		NodeId:             "NodeB_Large",
		PocWeight:          20,
		TimeslotAllocation: []bool{true, false},
	}

	nodes := []*types.MLNodeInfo{nodeA, nodeB}
	
	// Simulation: Epoch 1 Allocation
	t.Log("--- Epoch 1 Allocation ---")
	// No previously safe nodes in Epoch 1
	prevSafe1 := make(map[string]bool)
	selected1 := getSmallestMLNodeWithPOCSLotFalse(nodes, prevSafe1)
	require.Equal(t, "NodeA_Small", selected1.NodeId, "Epoch 1 should pick NodeA (smallest)")
	selected1.TimeslotAllocation[1] = true
	t.Logf("Epoch 1 picked: %s", selected1.NodeId)

	// Simulation: Epoch 2 Allocation preparation
	// Identify previously safe nodes
	prevSafe2 := make(map[string]bool)
	for _, n := range nodes {
		if len(n.TimeslotAllocation) > 1 && n.TimeslotAllocation[1] {
			prevSafe2[n.NodeId] = true
		}
	}

	// Reset allocations (as done in setModelsForParticipants)
	for _, n := range nodes {
		n.TimeslotAllocation = []bool{true, false}
	}
	
	t.Log("--- Epoch 2 Allocation ---")
	// Epoch 2 should pick NodeB even though it's larger, because NodeA was safe previously
	selected2 := getSmallestMLNodeWithPOCSLotFalse(nodes, prevSafe2)
	
	require.Equal(t, "NodeB_Large", selected2.NodeId, "Epoch 2 should pick NodeB (rotation)")
	selected2.TimeslotAllocation[1] = true
	t.Logf("Epoch 2 picked: %s", selected2.NodeId)

	require.NotEqual(t, selected1.NodeId, selected2.NodeId, "Rotation should have occurred")
	t.Log("[Verification] Rotation confirmed: NodeB was picked to avoid hogging.")
}

func TestInferenceSlotRotation_SingleNode(t *testing.T) {
	// Setup: Participant with only 1 node
	nodeA := &types.MLNodeInfo{
		NodeId:             "OnlyNode",
		PocWeight:          10,
		TimeslotAllocation: []bool{true, false},
	}
	nodes := []*types.MLNodeInfo{nodeA}

	t.Log("--- Single Node Epoch 1 ---")
	selected1 := getSmallestMLNodeWithPOCSLotFalse(nodes, nil)
	require.Equal(t, "OnlyNode", selected1.NodeId)
	selected1.TimeslotAllocation[1] = true

	// Epoch 2
	prevSafe := map[string]bool{"OnlyNode": true}
	nodeA.TimeslotAllocation = []bool{true, false}

	t.Log("--- Single Node Epoch 2 ---")
	selected2 := getSmallestMLNodeWithPOCSLotFalse(nodes, prevSafe)
	require.Equal(t, "OnlyNode", selected2.NodeId, "Should still pick the node if it's the only one")
	t.Log("[Verification] Single node case handled correctly (rotation ignored if no alternatives).")
}
