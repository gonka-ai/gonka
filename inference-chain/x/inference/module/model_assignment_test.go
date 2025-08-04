package inference

import (
	"context"
	"testing"

	"github.com/productscience/inference/x/inference/keeper"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Mock Keeper
type mockKeeperForModelAssigner struct {
	hardwareNodes    map[string]*types.HardwareNodes
	governanceModels []types.Model
}

func (m *mockKeeperForModelAssigner) GetGovernanceModelsSorted(ctx context.Context) ([]*types.Model, error) {
	return keeper.ValuesToPointers(m.governanceModels), nil
}

func (m *mockKeeperForModelAssigner) GetHardwareNodes(ctx context.Context, participantId string) (*types.HardwareNodes, bool) {
	nodes, found := m.hardwareNodes[participantId]
	return nodes, found
}

func (m *mockKeeperForModelAssigner) GetActiveParticipants(ctx context.Context, epochId uint64) (val types.ActiveParticipants, found bool) {
	// Not implemented for this mock
	return types.ActiveParticipants{}, false
}

// Mock Logger
type mockLogger struct{}

func (m mockLogger) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{})  {}
func (m mockLogger) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {}
func (m mockLogger) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{})  {}
func (m mockLogger) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {}

func TestSetModelsForParticipants_OneModelTwoNodes_Bug(t *testing.T) {
	// 1. Setup
	ctx := context.Background()
	participantAddress := "gonka1xmwh48ugfvd2ktmy0t90ueuzqxdk4g0anwe3v6"
	modelID := "Qwen/QwQ-32B"

	models := []types.Model{
		{
			ProposedBy:             "genesis",
			Id:                     "Qwen/QwQ-32B",
			UnitsOfComputePerToken: 1000,
			HfRepo:                 "Qwen/QwQ-32B",
			HfCommit:               "976055f8c83f394f35dbd3ab09a285a984907bd0",
			ModelArgs:              []string{"--quantization", "fp8", "-kv-cache-dtype", "fp8"},
			VRam:                   32,
			ThroughputPerNonce:     1000,
			ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
		},
		{
			ProposedBy:             "genesis",
			Id:                     "Qwen/Qwen2.5-7B-Instruct",
			UnitsOfComputePerToken: 100,
			HfRepo:                 "Qwen/Qwen2.5-7B-Instruct",
			HfCommit:               "a09a35458c702b33eeacc393d103063234e8bc28",
			ModelArgs:              []string{"--quantization", "fp8"},
			VRam:                   16,
			ThroughputPerNonce:     10000,
			ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
		},
	}
	// Mock Keeper setup
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: models,
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "mlnode1", Models: []string{modelID}},
					{LocalId: "mlnode2", Models: []string{modelID}},
				},
			},
		},
	}

	// Model Assigner
	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})

	// Participant data setup
	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{modelID},
			MlNodes: []*types.ModelMLNodes{ // This is the initial state before model assignment
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "mlnode1", PocWeight: 29},
						{NodeId: "mlnode2", PocWeight: 28},
					},
				},
			},
		},
	}

	upcomingEpoch := types.Epoch{Index: 1}

	// 2. Execute
	modelAssigner.setModelsForParticipants(ctx, participants, upcomingEpoch)

	// 3. Assert
	participant := participants[0]

	// The bug causes the model list to have 1 model, but the ml_nodes list has 2 entries.
	// One for the assigned model, and one for the "overflow" node.
	require.Len(t, participant.Models, 1, "Should have one supported model")
	require.Equal(t, modelID, participant.Models[0], "The supported model should be correct")

	require.Len(t, participant.MlNodes, 1, "Should have one MLNode groups corresponding to the model: "+modelID)

	// Check first group (assigned model)
	modelGroup := participant.MlNodes[0]
	require.Len(t, modelGroup.MlNodes, 2, "The model-specific group should have two nodes")

	// Verify that both nodes are in the same group and have the correct timeslot allocations.
	assertNodeInGroup(t, modelGroup.MlNodes, "mlnode1")
	assertNodeInGroup(t, modelGroup.MlNodes, "mlnode2")

	// Verify that one node is allocated for PoC and the other is not.
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, false}, 1)
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, true}, 1)
}

// assertNodeInGroup checks if a node with the given ID exists in the list of nodes.
func assertNodeInGroup(t *testing.T, nodes []*types.MLNodeInfo, nodeID string) {
	t.Helper()
	found := false
	for _, node := range nodes {
		if node.NodeId == nodeID {
			found = true
			break
		}
	}
	require.True(t, found, "Node with ID %s not found in the group", nodeID)
}

// assertTimeslotAllocationCount checks if there are exactly `expectedCount` nodes
// with the given timeslot allocation.
func assertTimeslotAllocationCount(t *testing.T, nodes []*types.MLNodeInfo, allocation []bool, expectedCount int) {
	t.Helper()
	count := 0
	for _, node := range nodes {
		if equalBoolSlice(node.TimeslotAllocation, allocation) {
			count++
		}
	}
	require.Equal(t, expectedCount, count, "Expected %d nodes with timeslot allocation %v, but found %d", expectedCount, allocation, count)
}

// equalBoolSlice compares two boolean slices for equality.
func equalBoolSlice(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSetModelsForParticipants_OneNodeOneModel(t *testing.T) {
	// 1. Setup
	ctx := context.Background()
	participantAddress := "gonka1xmwh48ugfvd2ktmy0t90ueuzqxdk4g0anwe3v6"
	modelID := "Qwen/Qwen2.5-7B-Instruct"

	models := []types.Model{
		{
			ProposedBy: "genesis",
			Id:         modelID,
			VRam:       16,
		},
	}
	// Mock Keeper setup
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: models,
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "mlnode1", Models: []string{modelID}},
				},
			},
		},
	}

	// Model Assigner
	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})

	// Participant data setup
	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{modelID},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "mlnode1", PocWeight: 29},
					},
				},
			},
		},
	}

	upcomingEpoch := types.Epoch{Index: 1}

	// 2. Execute
	modelAssigner.setModelsForParticipants(ctx, participants, upcomingEpoch)

	// 3. Assert
	participant := participants[0]

	require.Len(t, participant.Models, 1, "Should have one supported model")
	require.Equal(t, modelID, participant.Models[0], "The supported model should be correct")

	require.Len(t, participant.MlNodes, 1, "Should have one MLNode group corresponding to the model")

	modelGroup := participant.MlNodes[0]
	require.Len(t, modelGroup.MlNodes, 1, "The model-specific group should have one node")

	assertNodeInGroup(t, modelGroup.MlNodes, "mlnode1")
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, false}, 1)
}
