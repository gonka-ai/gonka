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

func TestGetSmallestMLNodeWithPOCSLotFalse_EdgeCases(t *testing.T) {
	t.Run("empty list returns nil", func(t *testing.T) {
		require.Nil(t, getSmallestMLNodeWithPOCSLotFalse(nil, nil))
	})

	t.Run("all nodes already allocated returns nil", func(t *testing.T) {
		nodes := []*types.MLNodeInfo{
			{NodeId: "N1", PocWeight: 100, TimeslotAllocation: []bool{true, true}},
			{NodeId: "N2", PocWeight: 200, TimeslotAllocation: []bool{true, true}},
		}
		require.Nil(t, getSmallestMLNodeWithPOCSLotFalse(nodes, nil))
	})

	t.Run("ignores nodes with short TimeslotAllocation", func(t *testing.T) {
		nodes := []*types.MLNodeInfo{
			{NodeId: "N1", PocWeight: 100, TimeslotAllocation: []bool{}},
			{NodeId: "N2", PocWeight: 90, TimeslotAllocation: []bool{true}},
			{NodeId: "N3", PocWeight: 80, TimeslotAllocation: []bool{true, false}},
		}
		selected := getSmallestMLNodeWithPOCSLotFalse(nodes, nil)
		require.NotNil(t, selected)
		require.Equal(t, "N3", selected.NodeId)
	})

	t.Run("previouslySafeIds nil selects smallest non-safe", func(t *testing.T) {
		nodes := []*types.MLNodeInfo{
			{NodeId: "N1", PocWeight: 120, TimeslotAllocation: []bool{true, false}},
			{NodeId: "N2", PocWeight: 90, TimeslotAllocation: []bool{true, false}},
		}
		selected := getSmallestMLNodeWithPOCSLotFalse(nodes, nil)
		require.NotNil(t, selected)
		require.Equal(t, "N2", selected.NodeId)
	})

	t.Run("prefers non-safe even if safe is lighter", func(t *testing.T) {
		nodes := []*types.MLNodeInfo{
			{NodeId: "N1", PocWeight: 50, TimeslotAllocation: []bool{true, false}},
			{NodeId: "N2", PocWeight: 60, TimeslotAllocation: []bool{true, false}},
		}
		prevSafe := map[string]bool{"N1": true}
		selected := getSmallestMLNodeWithPOCSLotFalse(nodes, prevSafe)
		require.NotNil(t, selected)
		require.Equal(t, "N2", selected.NodeId)
	})

	t.Run("all candidates are safe picks smallest safe", func(t *testing.T) {
		nodes := []*types.MLNodeInfo{
			{NodeId: "N1", PocWeight: 70, TimeslotAllocation: []bool{true, false}},
			{NodeId: "N2", PocWeight: 90, TimeslotAllocation: []bool{true, false}},
		}
		prevSafe := map[string]bool{"N1": true, "N2": true}
		selected := getSmallestMLNodeWithPOCSLotFalse(nodes, prevSafe)
		require.NotNil(t, selected)
		require.Equal(t, "N1", selected.NodeId)
	})

	t.Run("equal weights uses deterministic order", func(t *testing.T) {
		nodes := []*types.MLNodeInfo{
			{NodeId: "N1", PocWeight: 100, TimeslotAllocation: []bool{true, false}},
			{NodeId: "N2", PocWeight: 100, TimeslotAllocation: []bool{true, false}},
		}
		selected := getSmallestMLNodeWithPOCSLotFalse(nodes, nil)
		require.NotNil(t, selected)
		require.Equal(t, "N1", selected.NodeId)
	})

	t.Run("rotation across multiple candidates", func(t *testing.T) {
		nodes := []*types.MLNodeInfo{
			{NodeId: "N1", PocWeight: 100, TimeslotAllocation: []bool{true, false}},
			{NodeId: "N2", PocWeight: 200, TimeslotAllocation: []bool{true, false}},
			{NodeId: "N3", PocWeight: 300, TimeslotAllocation: []bool{true, false}},
		}

		// First pick should be smallest (N1)
		selected1 := getSmallestMLNodeWithPOCSLotFalse(nodes, nil)
		require.NotNil(t, selected1)
		require.Equal(t, "N1", selected1.NodeId)

		// Mark N1 as previously safe; next pick should be N2
		prevSafe := map[string]bool{"N1": true}
		selected2 := getSmallestMLNodeWithPOCSLotFalse(nodes, prevSafe)
		require.NotNil(t, selected2)
		require.Equal(t, "N2", selected2.NodeId)

		// Mark N1 and N2 safe; next pick should be N3
		prevSafe["N2"] = true
		selected3 := getSmallestMLNodeWithPOCSLotFalse(nodes, prevSafe)
		require.NotNil(t, selected3)
		require.Equal(t, "N3", selected3.NodeId)
	})
}
