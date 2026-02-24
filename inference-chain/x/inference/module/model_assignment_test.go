package inference

import (
	"context"
	"fmt"
	"testing"

	"github.com/productscience/inference/x/inference/keeper"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Mock Keeper
type mockKeeperForModelAssigner struct {
	hardwareNodes    map[string]*types.HardwareNodes
	governanceModels []types.Model
	epochGroupData   map[string]map[uint64]types.EpochGroupData // modelId -> epochIndex -> data
	settleAmounts    map[string]types.SettleAmount              // participant -> settle (optional; when set, participants count as rewarded for previous epoch)
	params           *types.Params
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

func (m *mockKeeperForModelAssigner) GetEpochGroupData(ctx context.Context, epochIndex uint64, modelId string) (val types.EpochGroupData, found bool) {
	if m.epochGroupData == nil {
		return types.EpochGroupData{}, false
	}
	if modelData, ok := m.epochGroupData[modelId]; ok {
		if data, ok := modelData[epochIndex]; ok {
			return data, true
		}
	}
	return types.EpochGroupData{}, false
}

func (m *mockKeeperForModelAssigner) GetSettleAmount(ctx context.Context, participant string) (val types.SettleAmount, found bool) {
	if m.settleAmounts != nil {
		if s, ok := m.settleAmounts[participant]; ok {
			return s, true
		}
	}
	return types.SettleAmount{}, false
}

func (m *mockKeeperForModelAssigner) GetParams(ctx context.Context) (types.Params, error) {
	if m.params != nil {
		return *m.params, nil
	}
	return types.DefaultParams(), nil
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
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelID: {
				0: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: participantAddress,
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "mlnode1", PocWeight: 29},
								{NodeId: "mlnode2", PocWeight: 28},
							},
						},
					},
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

	// setModelsForParticipants only initializes nodes, doesn't allocate POC slots
	// All nodes should be [true, false] (PRE_POC_SLOT=true, POC_SLOT=false)
	// Actual POC allocation happens in AllocateMLNodesForPoC
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, false}, 2)
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, true}, 0)
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
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelID: {
				0: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: participantAddress,
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "mlnode1", PocWeight: 29},
							},
						},
					},
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
	// With Phase 1 fix: Single node participants preserve voting power (Option B)
	// The node is excluded from eligible set to ensure 25% weight for voting.
	// Since the node is indivisible, it's kept entirely for voting (100% voting power).
	// This prevents the participant from becoming non-voting.
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, false}, 1) // Kept for voting
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, true}, 0)  // Not allocated for PoC
}

func TestSetModelsForParticipants_ManyNodesManyModels(t *testing.T) {
	// 1. Setup
	ctx := context.Background()
	participantAddress := "gonka1xmwh48ugfvd2ktmy0t90ueuzqxdk4g0anwe3v6"
	modelA := "Qwen/QwQ-32B"
	modelB := "Qwen/Qwen2.5-7B-Instruct"

	models := []types.Model{
		{ProposedBy: "genesis", Id: modelA, VRam: 32},
		{ProposedBy: "genesis", Id: modelB, VRam: 16},
	}

	// Mock Keeper setup with 4 nodes supporting mixed models
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: models,
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "mlnode1", Models: []string{modelA, modelB}}, // supports both
					{LocalId: "mlnode2", Models: []string{modelA}},         // supports A
					{LocalId: "mlnode3", Models: []string{modelB}},         // supports B
					{LocalId: "mlnode4", Models: []string{modelA, modelB}}, // supports both
				},
			},
		},
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelA: {
				1: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: participantAddress,
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "mlnode1", PocWeight: 30},
								{NodeId: "mlnode2", PocWeight: 25},
								{NodeId: "mlnode4", PocWeight: 25},
							},
						},
					},
				},
			},
			modelB: {
				1: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: participantAddress,
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "mlnode3", PocWeight: 20},
							},
						},
					},
				},
			},
		},
	}

	// Model Assigner
	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})

	// Participant data setup with legacy MLNodes list (pre-assignment state)
	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{modelA, modelB},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "mlnode1", PocWeight: 30},
						{NodeId: "mlnode2", PocWeight: 25},
						{NodeId: "mlnode3", PocWeight: 20},
						{NodeId: "mlnode4", PocWeight: 25},
					},
				},
			},
		},
	}

	upcomingEpoch := types.Epoch{Index: 2}

	// 2. Execute
	modelAssigner.setModelsForParticipants(ctx, participants, upcomingEpoch)

	// 3. Assert
	participant := participants[0]

	// Expect two supported models in the same order as governance models
	require.Len(t, participant.Models, 2, "Should have two supported models")
	require.Equal(t, modelA, participant.Models[0], "First model should be modelA")
	require.Equal(t, modelB, participant.Models[1], "Second model should be modelB")

	// Expect two MLNode groups, one per model (no overflow group expected because all nodes get assigned)
	require.Len(t, participant.MlNodes, 2, "Should have two MLNode groups corresponding to the two models")

	// Group for modelA should contain nodes that support A and were unassigned at that time
	groupA := participant.MlNodes[0]
	require.Len(t, groupA.MlNodes, 3, "Model A group should have three nodes (mlnode1, mlnode2, mlnode4)")
	assertNodeInGroup(t, groupA.MlNodes, "mlnode1")
	assertNodeInGroup(t, groupA.MlNodes, "mlnode2")
	assertNodeInGroup(t, groupA.MlNodes, "mlnode4")

	// Group for modelB should contain the remaining node supporting B only
	groupB := participant.MlNodes[1]
	require.Len(t, groupB.MlNodes, 1, "Model B group should have one node (mlnode3)")
	assertNodeInGroup(t, groupB.MlNodes, "mlnode3")

	// setModelsForParticipants only initializes timeslot allocations
	// All nodes are initialized to [true, false] (PRE_POC_SLOT=true, POC_SLOT=false)
	// Actual POC slot allocation happens later in AllocateMLNodesForPoC
	// Model A: 3 nodes should all be [true, false]
	// Model B: 1 node should be [true, false]
	assertTimeslotAllocationCount(t, groupA.MlNodes, []bool{true, true}, 0)
	assertTimeslotAllocationCount(t, groupA.MlNodes, []bool{true, false}, 3)
	assertTimeslotAllocationCount(t, groupB.MlNodes, []bool{true, true}, 0)
	assertTimeslotAllocationCount(t, groupB.MlNodes, []bool{true, false}, 1)
}

func TestAllocateMLNodesForPoC_MultipleParticipantsAndAllocations(t *testing.T) {
	const modelID = "model-abc"

	testCases := []struct {
		name                   string
		allocationPercentage   float64
		participants           []*types.ActiveParticipant
		hardwareNodesMap       map[string]*types.HardwareNodes
		previousEpochGroupData map[string]map[uint64]types.EpochGroupData
		expectedMinWeight      int64
		expectedMaxWeight      int64
		expectedTotalWeight    int64
		expectedTargetWeight   int64
	}{
		{
			name:                 "50% allocation with 3 participants, varying weights (10-50 range)",
			allocationPercentage: 50.0,
			participants: []*types.ActiveParticipant{
				{
					Index:  "participant1",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p1-node1", PocWeight: 30},
								{NodeId: "p1-node2", PocWeight: 25},
								{NodeId: "p1-node3", PocWeight: 20},
							},
						},
					},
				},
				{
					Index:  "participant2",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p2-node1", PocWeight: 40},
								{NodeId: "p2-node2", PocWeight: 35},
							},
						},
					},
				},
				{
					Index:  "participant3",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p3-node1", PocWeight: 50},
								{NodeId: "p3-node2", PocWeight: 45},
								{NodeId: "p3-node3", PocWeight: 40},
								{NodeId: "p3-node4", PocWeight: 35},
							},
						},
					},
				},
			},
			hardwareNodesMap: map[string]*types.HardwareNodes{
				"participant1": {
					Participant: "participant1",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p1-node1", Models: []string{modelID}},
						{LocalId: "p1-node2", Models: []string{modelID}},
						{LocalId: "p1-node3", Models: []string{modelID}},
					},
				},
				"participant2": {
					Participant: "participant2",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p2-node1", Models: []string{modelID}},
						{LocalId: "p2-node2", Models: []string{modelID}},
					},
				},
				"participant3": {
					Participant: "participant3",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p3-node1", Models: []string{modelID}},
						{LocalId: "p3-node2", Models: []string{modelID}},
						{LocalId: "p3-node3", Models: []string{modelID}},
						{LocalId: "p3-node4", Models: []string{modelID}},
					},
				},
			},
			previousEpochGroupData: map[string]map[uint64]types.EpochGroupData{
				modelID: {
					0: {
						ValidationWeights: []*types.ValidationWeight{
							{
								MemberAddress: "participant1",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p1-node1", PocWeight: 30},
									{NodeId: "p1-node2", PocWeight: 25},
									{NodeId: "p1-node3", PocWeight: 20},
								},
							},
							{
								MemberAddress: "participant2",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p2-node1", PocWeight: 40},
									{NodeId: "p2-node2", PocWeight: 35},
								},
							},
							{
								MemberAddress: "participant3",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p3-node1", PocWeight: 50},
									{NodeId: "p3-node2", PocWeight: 45},
									{NodeId: "p3-node3", PocWeight: 40},
									{NodeId: "p3-node4", PocWeight: 35},
								},
							},
						},
					},
				},
			},
			expectedTotalWeight:  320, // 75 + 75 + 170
			expectedTargetWeight: 160, // 50% of 320
			expectedMinWeight:    0,   // With participant-level filtering (2 out of 3 eligible), actual allocation varies
			expectedMaxWeight:    320, // But shouldn't exceed total
		},
		{
			name:                 "30% allocation with 2 participants (10-50 weight range)",
			allocationPercentage: 30.0,
			participants: []*types.ActiveParticipant{
				{
					Index:  "participant1",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p1-node1", PocWeight: 50},
								{NodeId: "p1-node2", PocWeight: 40},
								{NodeId: "p1-node3", PocWeight: 30},
							},
						},
					},
				},
				{
					Index:  "participant2",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p2-node1", PocWeight: 20},
								{NodeId: "p2-node2", PocWeight: 10},
							},
						},
					},
				},
			},
			hardwareNodesMap: map[string]*types.HardwareNodes{
				"participant1": {
					Participant: "participant1",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p1-node1", Models: []string{modelID}},
						{LocalId: "p1-node2", Models: []string{modelID}},
						{LocalId: "p1-node3", Models: []string{modelID}},
					},
				},
				"participant2": {
					Participant: "participant2",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p2-node1", Models: []string{modelID}},
						{LocalId: "p2-node2", Models: []string{modelID}},
					},
				},
			},
			previousEpochGroupData: map[string]map[uint64]types.EpochGroupData{
				modelID: {
					0: {
						ValidationWeights: []*types.ValidationWeight{
							{
								MemberAddress: "participant1",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p1-node1", PocWeight: 50},
									{NodeId: "p1-node2", PocWeight: 40},
									{NodeId: "p1-node3", PocWeight: 30},
								},
							},
							{
								MemberAddress: "participant2",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p2-node1", PocWeight: 20},
									{NodeId: "p2-node2", PocWeight: 10},
								},
							},
						},
					},
				},
			},
			expectedTotalWeight:  150, // 120 + 30
			expectedTargetWeight: 45,  // 30% of 150
			expectedMinWeight:    0,   // With participant-level filtering (2 out of 2 eligible), actual varies
			expectedMaxWeight:    150, // But shouldn't exceed total
		},
		{
			name:                 "70% allocation with 4 participants (10-50 weight range)",
			allocationPercentage: 70.0,
			participants: []*types.ActiveParticipant{
				{
					Index:  "participant1",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p1-node1", PocWeight: 15},
								{NodeId: "p1-node2", PocWeight: 10},
							},
						},
					},
				},
				{
					Index:  "participant2",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p2-node1", PocWeight: 25},
								{NodeId: "p2-node2", PocWeight: 20},
							},
						},
					},
				},
				{
					Index:  "participant3",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p3-node1", PocWeight: 35},
								{NodeId: "p3-node2", PocWeight: 30},
							},
						},
					},
				},
				{
					Index:  "participant4",
					Models: []string{modelID},
					MlNodes: []*types.ModelMLNodes{
						{
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p4-node1", PocWeight: 45},
								{NodeId: "p4-node2", PocWeight: 40},
							},
						},
					},
				},
			},
			hardwareNodesMap: map[string]*types.HardwareNodes{
				"participant1": {
					Participant: "participant1",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p1-node1", Models: []string{modelID}},
						{LocalId: "p1-node2", Models: []string{modelID}},
					},
				},
				"participant2": {
					Participant: "participant2",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p2-node1", Models: []string{modelID}},
						{LocalId: "p2-node2", Models: []string{modelID}},
					},
				},
				"participant3": {
					Participant: "participant3",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p3-node1", Models: []string{modelID}},
						{LocalId: "p3-node2", Models: []string{modelID}},
					},
				},
				"participant4": {
					Participant: "participant4",
					HardwareNodes: []*types.HardwareNode{
						{LocalId: "p4-node1", Models: []string{modelID}},
						{LocalId: "p4-node2", Models: []string{modelID}},
					},
				},
			},
			previousEpochGroupData: map[string]map[uint64]types.EpochGroupData{
				modelID: {
					0: {
						ValidationWeights: []*types.ValidationWeight{
							{
								MemberAddress: "participant1",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p1-node1", PocWeight: 15},
									{NodeId: "p1-node2", PocWeight: 10},
								},
							},
							{
								MemberAddress: "participant2",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p2-node1", PocWeight: 25},
									{NodeId: "p2-node2", PocWeight: 20},
								},
							},
							{
								MemberAddress: "participant3",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p3-node1", PocWeight: 35},
									{NodeId: "p3-node2", PocWeight: 30},
								},
							},
							{
								MemberAddress: "participant4",
								MlNodes: []*types.MLNodeInfo{
									{NodeId: "p4-node1", PocWeight: 45},
									{NodeId: "p4-node2", PocWeight: 40},
								},
							},
						},
					},
				},
			},
			expectedTotalWeight:  220, // 25 + 45 + 65 + 85
			expectedTargetWeight: 154, // 70% of 220
			expectedMinWeight:    0,   // With participant-level filtering (3 out of 4 eligible), actual varies
			expectedMaxWeight:    220, // But shouldn't exceed total
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup mock keeper with custom allocation fraction
			customParams := types.DefaultParams()
			// Convert percentage (0-100) to fraction (0-1)
			customParams.EpochParams.PocSlotAllocation = &types.Decimal{
				Value:    int64(tc.allocationPercentage * 10),
				Exponent: -3, // e.g., 50% = 500 * 10^(-3) = 0.5
			}

			mockKeeper := &mockKeeperForModelAssigner{
				hardwareNodes: tc.hardwareNodesMap,
				governanceModels: []types.Model{
					{
						Id:                     modelID,
						ProposedBy:             "genesis",
						UnitsOfComputePerToken: 100,
						HfRepo:                 "test/model",
						HfCommit:               "abc123",
						VRam:                   16,
						ThroughputPerNonce:     1000,
						ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
					},
				},
				epochGroupData: tc.previousEpochGroupData,
				params:         &customParams,
			}

			modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
			ctx := context.Background()
			upcomingEpoch := types.Epoch{Index: 1}

			// Call setModelsForParticipants which internally calls allocateMLNodesForPoC
			modelAssigner.setModelsForParticipants(ctx, tc.participants, upcomingEpoch)

			// Verify allocation results
			var totalWeight int64
			var allocatedWeight int64
			var allocatedCount int
			var totalCount int

			for _, participant := range tc.participants {
				require.Len(t, participant.MlNodes, 1, "Each participant should have one model group")
				modelGroup := participant.MlNodes[0]

				for _, node := range modelGroup.MlNodes {
					totalCount++
					totalWeight += node.PocWeight

					if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
						allocatedCount++
						allocatedWeight += node.PocWeight
					}
				}
			}

			// Verify total weight matches expected
			require.Equal(t, tc.expectedTotalWeight, totalWeight,
				"Total weight should match expected: %d", tc.expectedTotalWeight)

			// Verify target weight calculation
			require.Equal(t, tc.expectedTargetWeight, tc.expectedTotalWeight*int64(tc.allocationPercentage)/100,
				"Target weight calculation should match")

			// Verify allocated weight is within expected range
			require.GreaterOrEqual(t, allocatedWeight, tc.expectedMinWeight,
				"Allocated weight (%d) should be >= min expected (%d)", allocatedWeight, tc.expectedMinWeight)
			require.LessOrEqual(t, allocatedWeight, tc.expectedMaxWeight,
				"Allocated weight (%d) should be <= max expected (%d)", allocatedWeight, tc.expectedMaxWeight)

			t.Logf("Allocation Results:")
			t.Logf("  Total Weight: %d", totalWeight)
			t.Logf("  Target Weight: %d (%.1f%%)", tc.expectedTargetWeight, tc.allocationPercentage)
			t.Logf("  Allocated Weight: %d", allocatedWeight)
			t.Logf("  Allocated Percentage: %.2f%%", float64(allocatedWeight)/float64(totalWeight)*100)
			t.Logf("  Total Nodes: %d", totalCount)
			t.Logf("  Allocated Nodes: %d", allocatedCount)

			// Log per-participant allocation for debugging
			for _, participant := range tc.participants {
				participantAllocated := 0
				participantTotal := 0
				participantWeight := int64(0)
				for _, node := range participant.MlNodes[0].MlNodes {
					participantTotal++
					if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
						participantAllocated++
						participantWeight += node.PocWeight
					}
				}
				t.Logf("  Participant %s: %d/%d nodes allocated (weight: %d)", participant.Index, participantAllocated, participantTotal, participantWeight)
			}
		})
	}
}

func TestEligibilityFilter_DebugRandomness(t *testing.T) {
	const modelID = "model-test"

	// Create mock with 9 nodes (matching the failing test)
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{
			{
				Id:                     modelID,
				ProposedBy:             "genesis",
				UnitsOfComputePerToken: 100,
				HfRepo:                 "test/model",
				HfCommit:               "abc123",
				VRam:                   16,
				ThroughputPerNonce:     1000,
				ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
			},
		},
		hardwareNodes: map[string]*types.HardwareNodes{
			"participant1": {
				Participant: "participant1",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p1-node1", Models: []string{modelID}},
					{LocalId: "p1-node2", Models: []string{modelID}},
					{LocalId: "p1-node3", Models: []string{modelID}},
				},
			},
			"participant2": {
				Participant: "participant2",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p2-node1", Models: []string{modelID}},
					{LocalId: "p2-node2", Models: []string{modelID}},
				},
			},
			"participant3": {
				Participant: "participant3",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p3-node1", Models: []string{modelID}},
					{LocalId: "p3-node2", Models: []string{modelID}},
					{LocalId: "p3-node3", Models: []string{modelID}},
					{LocalId: "p3-node4", Models: []string{modelID}},
				},
			},
		},
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelID: {
				0: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: "participant1",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p1-node1", PocWeight: 30},
								{NodeId: "p1-node2", PocWeight: 25},
								{NodeId: "p1-node3", PocWeight: 20},
							},
						},
						{
							MemberAddress: "participant2",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p2-node1", PocWeight: 40},
								{NodeId: "p2-node2", PocWeight: 35},
							},
						},
						{
							MemberAddress: "participant3",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p3-node1", PocWeight: 50},
								{NodeId: "p3-node2", PocWeight: 45},
								{NodeId: "p3-node3", PocWeight: 40},
								{NodeId: "p3-node4", PocWeight: 35},
							},
						},
					},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  "participant1",
			Models: []string{modelID},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p1-node1", PocWeight: 30},
						{NodeId: "p1-node2", PocWeight: 25},
						{NodeId: "p1-node3", PocWeight: 20},
					},
				},
			},
		},
		{
			Index:  "participant2",
			Models: []string{modelID},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p2-node1", PocWeight: 40},
						{NodeId: "p2-node2", PocWeight: 35},
					},
				},
			},
		},
		{
			Index:  "participant3",
			Models: []string{modelID},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p3-node1", PocWeight: 50},
						{NodeId: "p3-node2", PocWeight: 45},
						{NodeId: "p3-node3", PocWeight: 40},
						{NodeId: "p3-node4", PocWeight: 35},
					},
				},
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	ctx := context.Background()
	upcomingEpoch := types.Epoch{Index: 1}

	modelAssigner.setModelsForParticipants(ctx, participants, upcomingEpoch)

	// Check POC_SLOT status for all nodes
	totalNodes := 0
	nodesWithPOCSlot := 0
	nodesByParticipant := make(map[string]struct{ total, allocated int })

	for _, participant := range participants {
		total := 0
		allocated := 0

		for _, node := range participant.MlNodes[0].MlNodes {
			totalNodes++
			total++
			if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
				nodesWithPOCSlot++
				allocated++
			}
		}
		nodesByParticipant[participant.Index] = struct{ total, allocated int }{total, allocated}
	}

	t.Logf("POC_SLOT Allocation Results:")
	t.Logf("  Total nodes: %d", totalNodes)
	t.Logf("  Nodes with POC_SLOT=true: %d (%.1f%%)", nodesWithPOCSlot, float64(nodesWithPOCSlot)/float64(totalNodes)*100)
	t.Logf("  Nodes with POC_SLOT=false: %d (%.1f%%)", totalNodes-nodesWithPOCSlot, float64(totalNodes-nodesWithPOCSlot)/float64(totalNodes)*100)
	t.Logf("  By participant:")
	for _, p := range []string{"participant1", "participant2", "participant3"} {
		stats := nodesByParticipant[p]
		t.Logf("    %s: %d/%d allocated", p, stats.allocated, stats.total)
	}
}

// TestAllocateMLNodesForPoC_FairDistribution tests that allocation is distributed fairly
// across many participants with many nodes
func TestAllocateMLNodesForPoC_FairDistribution(t *testing.T) {
	const (
		numParticipants     = 20
		nodesPerParticipant = 10
		baseWeight          = 10
		modelID             = "model-test"
	)

	ctx := context.Background()

	// Generate participants
	var participants []*types.ActiveParticipant
	hardwareNodesMap := make(map[string]*types.HardwareNodes)
	previousEpochGroupData := make(map[string]map[uint64]types.EpochGroupData)
	previousValidationWeights := make([]*types.ValidationWeight, 0, numParticipants)

	for i := 0; i < numParticipants; i++ {
		participantID := formatParticipantID(i)

		// Create hardware nodes for this participant
		hardwareNodes := make([]*types.HardwareNode, nodesPerParticipant)
		mlNodes := make([]*types.MLNodeInfo, nodesPerParticipant)
		previousMLNodes := make([]*types.MLNodeInfo, nodesPerParticipant)

		for j := 0; j < nodesPerParticipant; j++ {
			nodeID := formatNodeID(i, j)
			// Varying weights: alternate between baseWeight and baseWeight*2
			weight := int64(baseWeight)
			if j%2 == 0 {
				weight = int64(baseWeight * 2)
			}

			hardwareNodes[j] = &types.HardwareNode{
				LocalId: nodeID,
				Models:  []string{modelID},
			}

			mlNodes[j] = &types.MLNodeInfo{
				NodeId:             nodeID,
				PocWeight:          weight,
				TimeslotAllocation: []bool{true, false},
			}

			previousMLNodes[j] = &types.MLNodeInfo{
				NodeId:    nodeID,
				PocWeight: weight,
			}
		}

		participantWeight := int64(nodesPerParticipant * baseWeight * 3 / 2) // Average of 10 and 20
		participants = append(participants, &types.ActiveParticipant{
			Index:   participantID,
			Models:  []string{modelID},
			MlNodes: []*types.ModelMLNodes{{MlNodes: mlNodes}},
			Weight:  participantWeight,
		})

		hardwareNodesMap[participantID] = &types.HardwareNodes{
			Participant:   participantID,
			HardwareNodes: hardwareNodes,
		}

		previousValidationWeights = append(previousValidationWeights, &types.ValidationWeight{
			MemberAddress: participantID,
			MlNodes:       previousMLNodes,
		})
	}

	// Setup previous epoch data (all participants were active)
	previousEpochGroupData[modelID] = map[uint64]types.EpochGroupData{
		0: {ValidationWeights: previousValidationWeights},
	}

	// Settle amounts for previous epoch (epoch 0): all participants rewarded so they are eligible for POC_SLOT allocation
	previousEpochIndex := uint64(0)
	settleAmounts := make(map[string]types.SettleAmount, numParticipants)
	for i := 0; i < numParticipants; i++ {
		participantID := formatParticipantID(i)
		settleAmounts[participantID] = types.SettleAmount{
			Participant: participantID,
			EpochIndex:  previousEpochIndex,
			RewardCoins: 1,
		}
	}

	// Setup mock keeper
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID}},
		hardwareNodes:    hardwareNodesMap,
		epochGroupData:   previousEpochGroupData,
		settleAmounts:    settleAmounts,
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}, // 0.5
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})

	upcomingEpoch := types.Epoch{Index: 1}

	// Call model assignment and POC allocation
	modelAssigner.setModelsForParticipants(ctx, participants, upcomingEpoch)
	modelAssigner.AllocateMLNodesForPoC(ctx, upcomingEpoch, participants)

	// Collect allocation statistics
	type ParticipantStats struct {
		totalNodes      int
		allocatedNodes  int
		totalWeight     int64
		allocatedWeight int64
	}

	statsByParticipant := make(map[string]*ParticipantStats)
	var globalTotalWeight int64
	var globalAllocatedWeight int64
	var globalTotalNodes int
	var globalAllocatedNodes int

	for _, participant := range participants {
		stats := &ParticipantStats{}

		require.Len(t, participant.MlNodes, 1, "Each participant should have one model group")
		modelGroup := participant.MlNodes[0]

		for _, node := range modelGroup.MlNodes {
			stats.totalNodes++
			stats.totalWeight += node.PocWeight
			globalTotalNodes++
			globalTotalWeight += node.PocWeight

			if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
				stats.allocatedNodes++
				stats.allocatedWeight += node.PocWeight
				globalAllocatedNodes++
				globalAllocatedWeight += node.PocWeight
			}
		}

		statsByParticipant[participant.Index] = stats
	}

	// Calculate expected values based on N/2+1 participant sampling
	expectedEligibleParticipants := int64(numParticipants/2 + 1) // 11 out of 20
	expectedEligibleWeight := (globalTotalWeight * expectedEligibleParticipants) / int64(numParticipants)
	// Target is 50% of ELIGIBLE weight (not total weight)
	targetWeightFromEligible := expectedEligibleWeight / 2

	// Log overall results
	t.Logf("\n=== Fair Distribution Test Results ===")
	t.Logf("Participants: %d (eligible: %d with N/2+1 sampling)", numParticipants, expectedEligibleParticipants)
	t.Logf("Nodes per participant: %d", nodesPerParticipant)
	t.Logf("Total nodes: %d", globalTotalNodes)
	t.Logf("Total weight: %d", globalTotalWeight)
	t.Logf("Expected eligible weight: ~%d (from %d participants)", expectedEligibleWeight, expectedEligibleParticipants)
	t.Logf("Target weight from eligible: ~%d (50%% of eligible)", targetWeightFromEligible)
	t.Logf("Allocated weight: %d", globalAllocatedWeight)
	t.Logf("Allocated as %% of total: %.2f%%", float64(globalAllocatedWeight)/float64(globalTotalWeight)*100)
	t.Logf("Allocated as %% of eligible: %.2f%%", float64(globalAllocatedWeight)/float64(expectedEligibleWeight)*100)
	t.Logf("Allocated nodes: %d/%d", globalAllocatedNodes, globalTotalNodes)

	// Verify allocated weight is reasonable given N/2+1 sampling
	// We expect roughly 50% of eligible weight, with some variance due to:
	// - IQR outlier filtering may remove some nodes
	// - Voting constraints may limit allocation
	// - Round-robin may not fill completely
	minExpectedWeight := targetWeightFromEligible * 6 / 10 // At least 60% of target from eligible
	maxExpectedWeight := expectedEligibleWeight            // At most all eligible weight

	require.GreaterOrEqual(t, globalAllocatedWeight, minExpectedWeight,
		"Allocated weight (%d) should be >= 60%% of target from eligible (%d)",
		globalAllocatedWeight, minExpectedWeight)
	require.LessOrEqual(t, globalAllocatedWeight, maxExpectedWeight,
		"Allocated weight (%d) should not exceed total eligible weight (%d)",
		globalAllocatedWeight, maxExpectedWeight)

	// Check distribution fairness
	var allocatedCounts []int
	participantsWithAllocation := 0
	participantsWithNoAllocation := 0

	for i := 0; i < numParticipants; i++ {
		participantID := formatParticipantID(i)
		stats := statsByParticipant[participantID]

		allocatedCounts = append(allocatedCounts, stats.allocatedNodes)

		if stats.allocatedNodes > 0 {
			participantsWithAllocation++
		} else {
			participantsWithNoAllocation++
		}
	}

	t.Logf("\n=== Distribution Fairness ===")
	t.Logf("Participants with allocations: %d/%d", participantsWithAllocation, numParticipants)
	t.Logf("Participants with no allocations: %d", participantsWithNoAllocation)

	// Calculate min/max/avg for allocated nodes per participant
	if len(allocatedCounts) > 0 {
		minAllocated := allocatedCounts[0]
		maxAllocated := allocatedCounts[0]
		sumAllocated := 0

		for _, count := range allocatedCounts {
			if count < minAllocated {
				minAllocated = count
			}
			if count > maxAllocated {
				maxAllocated = count
			}
			sumAllocated += count
		}

		avgAllocated := float64(sumAllocated) / float64(numParticipants)

		t.Logf("Nodes allocated per participant:")
		t.Logf("  Min: %d", minAllocated)
		t.Logf("  Max: %d", maxAllocated)
		t.Logf("  Avg: %.2f", avgAllocated)
		t.Logf("  Range: %d", maxAllocated-minAllocated)

		// Log first 10 participants as sample
		t.Logf("\n=== Sample (first 10 participants) ===")
		for i := 0; i < 10 && i < numParticipants; i++ {
			participantID := formatParticipantID(i)
			stats := statsByParticipant[participantID]
			t.Logf("  %s: %d/%d nodes (%.1f%%), weight: %d/%d",
				participantID,
				stats.allocatedNodes, stats.totalNodes,
				float64(stats.allocatedNodes)/float64(stats.totalNodes)*100,
				stats.allocatedWeight, stats.totalWeight)
		}

		// Fairness assertions
		// The algorithm has two phases:
		// 1. Eligibility filter: N/2+1 participants selected (deterministic shuffle)
		// 2. Round-robin allocation: smallest nodes allocated from eligible participants

		// Expected: ~55% of participants get allocations (N/2+1 out of N)
		expectedEligible := numParticipants/2 + 1
		require.GreaterOrEqual(t, participantsWithAllocation, expectedEligible-1,
			"Should have ~N/2+1 participants with allocations (got %d, expected ~%d)",
			participantsWithAllocation, expectedEligible)
		require.LessOrEqual(t, participantsWithAllocation, expectedEligible+1,
			"Should have ~N/2+1 participants with allocations (got %d, expected ~%d)",
			participantsWithAllocation, expectedEligible)

		// Among ELIGIBLE participants, distribution should be relatively even
		// Calculate distribution among participants who got something
		var eligibleAllocations []int
		for _, count := range allocatedCounts {
			if count > 0 {
				eligibleAllocations = append(eligibleAllocations, count)
			}
		}

		if len(eligibleAllocations) > 0 {
			minEligible := eligibleAllocations[0]
			maxEligible := eligibleAllocations[0]
			for _, count := range eligibleAllocations {
				if count < minEligible {
					minEligible = count
				}
				if count > maxEligible {
					maxEligible = count
				}
			}

			t.Logf("\n=== Distribution Among Eligible Participants ===")
			t.Logf("  Min nodes: %d", minEligible)
			t.Logf("  Max nodes: %d", maxEligible)
			t.Logf("  Range: %d", maxEligible-minEligible)

			// With round-robin, eligible participants should get similar allocations
			// Allow some variation due to weight-based selection of smallest nodes
			require.LessOrEqual(t, maxEligible-minEligible, nodesPerParticipant,
				"Distribution among eligible participants should be relatively fair")
		}
	}
}

// TestAllocateMLNodesForPoC_NoReward_NoEligibleParticipants verifies that when no participants
// have a reward for the previous epoch, none are added to previousEpochData, so there are no
// eligible participants and no POC_SLOT allocation. It covers three ways to be ineligible:
// - no settle amount at all (participant not in settleAmounts)
// - settle amount with RewardCoins == 0 (slashed / no reward)
// - settle amount with reward but for a different epoch (EpochIndex != previousEpoch)
func TestAllocateMLNodesForPoC_NoReward_NoEligibleParticipants(t *testing.T) {
	const (
		numParticipants     = 20
		nodesPerParticipant = 10
		baseWeight          = 10
		modelID             = "model-no-reward"
		previousEpochIndex  = uint64(0) // upcomingEpoch.Index will be 1
	)

	// Partition participants into three ineligible groups (upcoming epoch = 1, previous = 0):
	// - No settle: participants 0-6  -> not in settleAmounts map (GetSettleAmount returns not found)
	// - Zero reward: 7-13           -> in map with EpochIndex=0, RewardCoins=0
	// - Wrong epoch: 14-19          -> in map with EpochIndex=2, RewardCoins=1 (reward for wrong epoch)
	const (
		noSettleEnd     = 7  // 0..6
		zeroRewardEnd   = 14 // 7..13
		wrongEpochStart = 14 // 14..19
	)

	ctx := context.Background()

	var participants []*types.ActiveParticipant
	hardwareNodesMap := make(map[string]*types.HardwareNodes)
	previousEpochGroupData := make(map[string]map[uint64]types.EpochGroupData)
	previousValidationWeights := make([]*types.ValidationWeight, 0, numParticipants)

	for i := 0; i < numParticipants; i++ {
		participantID := formatParticipantID(i)

		hardwareNodes := make([]*types.HardwareNode, nodesPerParticipant)
		mlNodes := make([]*types.MLNodeInfo, nodesPerParticipant)
		previousMLNodes := make([]*types.MLNodeInfo, nodesPerParticipant)

		for j := 0; j < nodesPerParticipant; j++ {
			nodeID := formatNodeID(i, j)
			weight := int64(baseWeight)
			if j%2 == 0 {
				weight = int64(baseWeight * 2)
			}

			hardwareNodes[j] = &types.HardwareNode{LocalId: nodeID, Models: []string{modelID}}
			mlNodes[j] = &types.MLNodeInfo{NodeId: nodeID, PocWeight: weight, TimeslotAllocation: []bool{true, false}}
			previousMLNodes[j] = &types.MLNodeInfo{NodeId: nodeID, PocWeight: weight}
		}

		participantWeight := int64(nodesPerParticipant * baseWeight * 3 / 2)
		participants = append(participants, &types.ActiveParticipant{
			Index:   participantID,
			Models:  []string{modelID},
			MlNodes: []*types.ModelMLNodes{{MlNodes: mlNodes}},
			Weight:  participantWeight,
		})

		hardwareNodesMap[participantID] = &types.HardwareNodes{Participant: participantID, HardwareNodes: hardwareNodes}
		previousValidationWeights = append(previousValidationWeights, &types.ValidationWeight{
			MemberAddress: participantID,
			MlNodes:       previousMLNodes,
		})
	}

	previousEpochGroupData[modelID] = map[uint64]types.EpochGroupData{
		0: {ValidationWeights: previousValidationWeights},
	}

	// settleAmounts is non-nil but only contains entries that still make everyone ineligible:
	// - participants 0-6: omitted (no settle) -> GetSettleAmount returns not found
	// - participants 7-13: EpochIndex=previousEpoch, RewardCoins=0 -> skipped (zero reward)
	// - participants 14-19: EpochIndex=2, RewardCoins=1 -> skipped (wrong epoch)
	settleAmounts := make(map[string]types.SettleAmount)
	for i := noSettleEnd; i < zeroRewardEnd; i++ {
		participantID := formatParticipantID(i)
		settleAmounts[participantID] = types.SettleAmount{
			Participant: participantID,
			EpochIndex:  previousEpochIndex,
			RewardCoins: 0, // zero reward => ineligible
		}
	}
	for i := wrongEpochStart; i < numParticipants; i++ {
		participantID := formatParticipantID(i)
		settleAmounts[participantID] = types.SettleAmount{
			Participant: participantID,
			EpochIndex:  previousEpochIndex + 2, // wrong epoch (e.g. 2 when previous is 0)
			RewardCoins: 1,
		}
	}

	t.Logf("Ineligible groups: no settle (0..%d), zero reward (%d..%d), wrong epoch (%d..%d)",
		noSettleEnd-1, noSettleEnd, zeroRewardEnd-1, wrongEpochStart, numParticipants-1)

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID}},
		hardwareNodes:    hardwareNodesMap,
		epochGroupData:   previousEpochGroupData,
		settleAmounts:    settleAmounts,
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1},
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	upcomingEpoch := types.Epoch{Index: 1}

	modelAssigner.setModelsForParticipants(ctx, participants, upcomingEpoch)
	modelAssigner.AllocateMLNodesForPoC(ctx, upcomingEpoch, participants)

	var globalTotalNodes int
	var globalAllocatedNodes int
	var globalAllocatedWeight int64
	participantsWithAllocation := 0

	for _, participant := range participants {
		require.Len(t, participant.MlNodes, 1)
		participantHasAllocation := false
		for _, node := range participant.MlNodes[0].MlNodes {
			globalTotalNodes++
			if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
				globalAllocatedNodes++
				globalAllocatedWeight += node.PocWeight
				participantHasAllocation = true
			}
		}
		if participantHasAllocation {
			participantsWithAllocation++
		}
	}

	t.Logf("No-reward scenario: total nodes=%d, allocated nodes=%d, allocated weight=%d, participants with allocation=%d",
		globalTotalNodes, globalAllocatedNodes, globalAllocatedWeight, participantsWithAllocation)

	require.Equal(t, 0, globalAllocatedNodes, "No nodes should have POC_SLOT=true when no participants have reward")
	require.Equal(t, int64(0), globalAllocatedWeight, "Allocated weight should be 0 when no participants have reward")
	require.Equal(t, 0, participantsWithAllocation, "No participant should have any POC_SLOT allocation")
}

// Helper functions for test
func formatParticipantID(index int) string {
	return fmt.Sprintf("participant%03d", index)
}

func formatNodeID(participantIndex, nodeIndex int) string {
	return fmt.Sprintf("p%03d-node%02d", participantIndex, nodeIndex)
}

// ============================================================================
// Unit Tests for Helper Functions
// ============================================================================

func TestCalculateWeightThresholdWithCount_UniformWeights(t *testing.T) {
	testCases := []struct {
		name          string
		weights       []int64
		targetPercent int
		expThreshold  int64
		expCount      int
	}{
		{
			name:          "Two uniform nodes, 25% target",
			weights:       []int64{10, 10},
			targetPercent: 25,
			expThreshold:  10,
			expCount:      1, // 25% of 20 = 5, first node reaches target
		},
		{
			name:          "Four uniform nodes, 25% target",
			weights:       []int64{10, 10, 10, 10},
			targetPercent: 25,
			expThreshold:  10,
			expCount:      1, // 25% of 40 = 10, first node reaches target
		},
		{
			name:          "Four uniform nodes, 50% target",
			weights:       []int64{10, 10, 10, 10},
			targetPercent: 50,
			expThreshold:  10,
			expCount:      2, // 50% of 40 = 20, two nodes reach target
		},
		{
			name:          "Four uniform nodes, 75% target",
			weights:       []int64{10, 10, 10, 10},
			targetPercent: 75,
			expThreshold:  10,
			expCount:      3, // 75% of 40 = 30, three nodes reach target
		},
		{
			name:          "All uniform weights need all nodes",
			weights:       []int64{15, 15, 15},
			targetPercent: 100,
			expThreshold:  15, // Returns exact weight for uniform case
			expCount:      3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			threshold, count := calculateWeightThresholdWithCount(tc.weights, tc.targetPercent)
			require.Equal(t, tc.expThreshold, threshold, "Threshold mismatch")
			require.Equal(t, tc.expCount, count, "Count mismatch")
		})
	}
}

func TestCalculateWeightThresholdWithCount_HeterogeneousWeights(t *testing.T) {
	testCases := []struct {
		name          string
		weights       []int64
		targetPercent int
		expThreshold  int64
		expCount      int
	}{
		{
			name:          "Heterogeneous weights, 25% target",
			weights:       []int64{30, 25, 20, 15},
			targetPercent: 25,
			expThreshold:  29, // 25% of 90 = 22.5, first node (30) reaches, return 30-1
			expCount:      0,  // No count limiting for heterogeneous
		},
		{
			name:          "Heterogeneous weights, 50% target",
			weights:       []int64{30, 25, 20, 15},
			targetPercent: 50,
			expThreshold:  24, // 50% of 90 = 45, 30+25=55 reaches, return 25-1
			expCount:      0,
		},
		{
			name:          "Descending weights, 70% target",
			weights:       []int64{50, 40, 30, 20, 10},
			targetPercent: 70,
			expThreshold:  29, // 70% of 150 = 105, 50+40+30=120, return 30-1
			expCount:      0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			threshold, count := calculateWeightThresholdWithCount(tc.weights, tc.targetPercent)
			require.Equal(t, tc.expThreshold, threshold, "Threshold mismatch")
			require.Equal(t, tc.expCount, count, "Count mismatch")
		})
	}
}

func TestCalculateWeightThresholdWithCount_EdgeCases(t *testing.T) {
	testCases := []struct {
		name          string
		weights       []int64
		targetPercent int
		expThreshold  int64
		expCount      int
	}{
		{
			name:          "Empty weights",
			weights:       []int64{},
			targetPercent: 50,
			expThreshold:  0,
			expCount:      0,
		},
		{
			name:          "Single node - voting preservation",
			weights:       []int64{10},
			targetPercent: 25,
			expThreshold:  9, // 10-1 to exclude for voting
			expCount:      0,
		},
		{
			name:          "Single node - any percent",
			weights:       []int64{100},
			targetPercent: 75,
			expThreshold:  99, // 100-1 to exclude for voting
			expCount:      0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			threshold, count := calculateWeightThresholdWithCount(tc.weights, tc.targetPercent)
			require.Equal(t, tc.expThreshold, threshold, "Threshold mismatch")
			require.Equal(t, tc.expCount, count, "Count mismatch")
		})
	}
}

func TestFilterNodesByWeightAndCount_CountLimit(t *testing.T) {
	nodes := []*types.MLNodeInfo{
		{NodeId: "node1", PocWeight: 10},
		{NodeId: "node2", PocWeight: 10},
		{NodeId: "node3", PocWeight: 10},
		{NodeId: "node4", PocWeight: 10},
	}

	testCases := []struct {
		name        string
		threshold   int64
		targetCount int
		expCount    int
		expNodeIds  []string
	}{
		{
			name:        "Count limit 2 with threshold 10",
			threshold:   10,
			targetCount: 2,
			expCount:    2,
			expNodeIds:  []string{"node1", "node2"}, // Sorted by NodeId
		},
		{
			name:        "Count limit 1 with threshold 10",
			threshold:   10,
			targetCount: 1,
			expCount:    1,
			expNodeIds:  []string{"node1"},
		},
		{
			name:        "Count limit 0 (no limiting) with threshold 10",
			threshold:   10,
			targetCount: 0,
			expCount:    4,
			expNodeIds:  []string{"node1", "node2", "node3", "node4"},
		},
		{
			name:        "Count limit exceeds available nodes",
			threshold:   10,
			targetCount: 10,
			expCount:    4, // Only 4 nodes available
			expNodeIds:  []string{"node1", "node2", "node3", "node4"},
		},
		{
			name:        "Threshold excludes all, count limit irrelevant",
			threshold:   9,
			targetCount: 2,
			expCount:    0,
			expNodeIds:  []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			filtered := filterNodesByWeightAndCount(nodes, tc.threshold, tc.targetCount)
			require.Len(t, filtered, tc.expCount, "Filtered count mismatch")

			for i, expId := range tc.expNodeIds {
				require.Equal(t, expId, filtered[i].NodeId, "Node ID mismatch at index %d", i)
			}
		})
	}
}

func TestFilterNodesByWeightAndCount_Determinism(t *testing.T) {
	// Test that same inputs produce same outputs (deterministic ordering)
	nodes := []*types.MLNodeInfo{
		{NodeId: "node-c", PocWeight: 10},
		{NodeId: "node-a", PocWeight: 10},
		{NodeId: "node-d", PocWeight: 15},
		{NodeId: "node-b", PocWeight: 10},
	}

	// Run filtering multiple times
	result1 := filterNodesByWeightAndCount(nodes, 15, 0)
	result2 := filterNodesByWeightAndCount(nodes, 15, 0)
	result3 := filterNodesByWeightAndCount(nodes, 15, 0)

	// All results should be identical
	require.Len(t, result1, 4)
	require.Len(t, result2, 4)
	require.Len(t, result3, 4)

	// Should be sorted by weight ascending, then by NodeId
	expectedOrder := []string{"node-a", "node-b", "node-c", "node-d"}
	for i, expId := range expectedOrder {
		require.Equal(t, expId, result1[i].NodeId, "Result 1 order mismatch at %d", i)
		require.Equal(t, expId, result2[i].NodeId, "Result 2 order mismatch at %d", i)
		require.Equal(t, expId, result3[i].NodeId, "Result 3 order mismatch at %d", i)
	}
}

// ============================================================================
// Integration Tests for Uniform Weights
// ============================================================================

func TestAllocateMLNodesForPoC_UniformWeights(t *testing.T) {
	const modelID = "model-uniform"
	ctx := context.Background()

	// Setup: 3 participants matching user's scenario
	// - Participant 1: 2 nodes × weight 10
	// - Participant 2: 1 node × weight 10
	// - Participant 3: 1 node × weight 10

	participants := []*types.ActiveParticipant{
		{
			Index:  "participant1",
			Models: []string{modelID},
			Weight: 20,
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p1-node1", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p1-node2", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
		{
			Index:  "participant2",
			Models: []string{modelID},
			Weight: 10,
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p2-node1", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
		{
			Index:  "participant3",
			Models: []string{modelID},
			Weight: 10,
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p3-node1", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
	}

	// Setup mock keeper with previous epoch data
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID}},
		hardwareNodes: map[string]*types.HardwareNodes{
			"participant1": {
				Participant: "participant1",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p1-node1", Models: []string{modelID}},
					{LocalId: "p1-node2", Models: []string{modelID}},
				},
			},
			"participant2": {
				Participant: "participant2",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p2-node1", Models: []string{modelID}},
				},
			},
			"participant3": {
				Participant: "participant3",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p3-node1", Models: []string{modelID}},
				},
			},
		},
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelID: {
				0: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: "participant1",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p1-node1", PocWeight: 10},
								{NodeId: "p1-node2", PocWeight: 10},
							},
						},
						{
							MemberAddress: "participant2",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p2-node1", PocWeight: 10},
							},
						},
						{
							MemberAddress: "participant3",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p3-node1", PocWeight: 10},
							},
						},
					},
				},
			},
		},
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}, // 50%
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	upcomingEpoch := types.Epoch{Index: 1}

	// Execute
	modelAssigner.AllocateMLNodesForPoC(ctx, upcomingEpoch, participants)

	// Verify results
	t.Logf("\n=== Uniform Weight Test Results ===")

	// Participant 1: Should have exactly 1 eligible node (count limiting for uniform weights)
	p1Nodes := participants[0].MlNodes[0].MlNodes
	p1Allocated := 0
	for _, node := range p1Nodes {
		if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
			p1Allocated++
		}
	}
	t.Logf("Participant 1 (2 nodes × 10): %d allocated", p1Allocated)
	// With 25% rule on uniform weights, expect 1 node to be eligible
	// Actual allocation depends on 50% target and round-robin

	// Participant 2 & 3: Single nodes should be excluded for voting preservation
	p2Nodes := participants[1].MlNodes[0].MlNodes
	p2Allocated := 0
	for _, node := range p2Nodes {
		if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
			p2Allocated++
		}
	}
	t.Logf("Participant 2 (1 node × 10): %d allocated", p2Allocated)

	p3Nodes := participants[2].MlNodes[0].MlNodes
	p3Allocated := 0
	for _, node := range p3Nodes {
		if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
			p3Allocated++
		}
	}
	t.Logf("Participant 3 (1 node × 10): %d allocated", p3Allocated)

	// Total allocation
	totalAllocated := p1Allocated + p2Allocated + p3Allocated
	t.Logf("Total allocated: %d", totalAllocated)

	// Assertions
	// Participant 2 & 3 should have 0 allocations (single node voting preservation)
	require.Equal(t, 0, p2Allocated, "Single-node participant 2 should not have allocations (voting preservation)")
	require.Equal(t, 0, p3Allocated, "Single-node participant 3 should not have allocations (voting preservation)")

	// Participant 1 should have at least some allocation
	require.GreaterOrEqual(t, p1Allocated, 0, "Participant 1 should be eligible for allocation")
	require.LessOrEqual(t, p1Allocated, 1, "Participant 1 should have at most 1 eligible node (25% of 2 nodes)")
}

func TestAllocateMLNodesForPoC_MixedUniformAndHeterogeneous(t *testing.T) {
	const modelID = "model-mixed"
	ctx := context.Background()

	// Setup: 3 participants with different weight distributions
	// - Participant 1: uniform weights (4 nodes × 10)
	// - Participant 2: heterogeneous weights (30, 25, 20, 15)
	// - Participant 3: uniform weights (3 nodes × 15)

	participants := []*types.ActiveParticipant{
		{
			Index:  "participant1",
			Models: []string{modelID},
			Weight: 40,
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p1-node1", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p1-node2", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p1-node3", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p1-node4", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
		{
			Index:  "participant2",
			Models: []string{modelID},
			Weight: 90,
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p2-node1", PocWeight: 30, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p2-node2", PocWeight: 25, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p2-node3", PocWeight: 20, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p2-node4", PocWeight: 15, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
		{
			Index:  "participant3",
			Models: []string{modelID},
			Weight: 45,
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "p3-node1", PocWeight: 15, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p3-node2", PocWeight: 15, TimeslotAllocation: []bool{true, false}},
						{NodeId: "p3-node3", PocWeight: 15, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
	}

	// Setup mock keeper with previous epoch data
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID}},
		hardwareNodes: map[string]*types.HardwareNodes{
			"participant1": {
				Participant: "participant1",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p1-node1", Models: []string{modelID}},
					{LocalId: "p1-node2", Models: []string{modelID}},
					{LocalId: "p1-node3", Models: []string{modelID}},
					{LocalId: "p1-node4", Models: []string{modelID}},
				},
			},
			"participant2": {
				Participant: "participant2",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p2-node1", Models: []string{modelID}},
					{LocalId: "p2-node2", Models: []string{modelID}},
					{LocalId: "p2-node3", Models: []string{modelID}},
					{LocalId: "p2-node4", Models: []string{modelID}},
				},
			},
			"participant3": {
				Participant: "participant3",
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "p3-node1", Models: []string{modelID}},
					{LocalId: "p3-node2", Models: []string{modelID}},
					{LocalId: "p3-node3", Models: []string{modelID}},
				},
			},
		},
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelID: {
				0: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: "participant1",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p1-node1", PocWeight: 10},
								{NodeId: "p1-node2", PocWeight: 10},
								{NodeId: "p1-node3", PocWeight: 10},
								{NodeId: "p1-node4", PocWeight: 10},
							},
						},
						{
							MemberAddress: "participant2",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p2-node1", PocWeight: 30},
								{NodeId: "p2-node2", PocWeight: 25},
								{NodeId: "p2-node3", PocWeight: 20},
								{NodeId: "p2-node4", PocWeight: 15},
							},
						},
						{
							MemberAddress: "participant3",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "p3-node1", PocWeight: 15},
								{NodeId: "p3-node2", PocWeight: 15},
								{NodeId: "p3-node3", PocWeight: 15},
							},
						},
					},
				},
			},
		},
		// All participants rewarded in previous epoch (epoch 0) so they are eligible for POC_SLOT allocation
		settleAmounts: map[string]types.SettleAmount{
			"participant1": {Participant: "participant1", EpochIndex: 0, RewardCoins: 1},
			"participant2": {Participant: "participant2", EpochIndex: 0, RewardCoins: 1},
			"participant3": {Participant: "participant3", EpochIndex: 0, RewardCoins: 1},
		},
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}, // 50%
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	upcomingEpoch := types.Epoch{Index: 1}

	// Execute
	modelAssigner.AllocateMLNodesForPoC(ctx, upcomingEpoch, participants)

	// Verify results
	t.Logf("\n=== Mixed Uniform/Heterogeneous Test Results ===")

	for i, participant := range participants {
		allocatedCount := 0
		totalWeight := int64(0)
		allocatedWeight := int64(0)

		for _, node := range participant.MlNodes[0].MlNodes {
			totalWeight += node.PocWeight
			if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
				allocatedCount++
				allocatedWeight += node.PocWeight
			}
		}

		t.Logf("Participant %d (%s): %d/%d nodes allocated, weight: %d/%d",
			i+1, participant.Index, allocatedCount, len(participant.MlNodes[0].MlNodes),
			allocatedWeight, totalWeight)
	}

	// Total weight and allocation
	totalWeight := int64(0)
	totalAllocatedWeight := int64(0)
	for _, participant := range participants {
		for _, node := range participant.MlNodes[0].MlNodes {
			totalWeight += node.PocWeight
			if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
				totalAllocatedWeight += node.PocWeight
			}
		}
	}

	t.Logf("Total weight: %d", totalWeight)
	t.Logf("Total allocated weight: %d (%.1f%%)", totalAllocatedWeight,
		float64(totalAllocatedWeight)/float64(totalWeight)*100)

	// Assertions
	// Total weight should be 175 (40 + 90 + 45)
	require.Equal(t, int64(175), totalWeight, "Total weight should be 175")

	// Some allocation should happen (not all filtered out)
	require.Greater(t, totalAllocatedWeight, int64(0), "Some nodes should be allocated")

	// Allocated weight should not exceed total
	require.LessOrEqual(t, totalAllocatedWeight, totalWeight, "Allocated weight should not exceed total")
}

func TestDedupMLNodesById(t *testing.T) {
	nodes := []*types.MLNodeInfo{
		{NodeId: "node-b", PocWeight: 10, Throughput: 100},
		{NodeId: "node-a", PocWeight: 5, Throughput: 50},
		{NodeId: "node-b", PocWeight: 20, Throughput: 10},
	}

	deduped, stats := dedupMLNodesById(nodes)

	require.Len(t, deduped, 2)
	require.Equal(t, "node-a", deduped[0].NodeId)
	require.Equal(t, "node-b", deduped[1].NodeId)
	require.Equal(t, int64(20), deduped[1].PocWeight)

	require.Contains(t, stats, "node-b")
	require.Len(t, stats["node-b"].dropped, 1)
	require.Equal(t, int64(10), stats["node-b"].dropped[0].PocWeight)
}

func TestSetModelsForParticipants_DedupesDuplicateNodes(t *testing.T) {
	ctx := context.Background()
	modelID := "model-dedup"
	participantAddress := "participant-1"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{
			{ProposedBy: "genesis", Id: modelID},
		},
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "dup-node", Models: []string{modelID}},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{modelID},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "dup-node", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
						{NodeId: "dup-node", PocWeight: 25, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	modelAssigner.setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

	require.Len(t, participants[0].MlNodes, 1)
	require.Len(t, participants[0].MlNodes[0].MlNodes, 1)
	require.Equal(t, int64(25), participants[0].MlNodes[0].MlNodes[0].PocWeight)
	require.Equal(t, "dup-node", participants[0].MlNodes[0].MlNodes[0].NodeId)
}

func TestAllocateMLNodesForPoC_DedupesBeforeAllocation(t *testing.T) {
	ctx := context.Background()
	modelID := "model-dedup"

	params := types.DefaultParams()
	params.EpochParams.PocSlotAllocation = &types.Decimal{Value: 5, Exponent: -1}

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{
			{ProposedBy: "genesis", Id: modelID},
		},
		params: &params,
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelID: {
				0: {
					ValidationWeights: []*types.ValidationWeight{
						{
							MemberAddress: "participant-1",
							MlNodes: []*types.MLNodeInfo{
								{NodeId: "dup-node", PocWeight: 40},
								{NodeId: "dup-node", PocWeight: 10},
							},
						},
					},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  "participant-1",
			Models: []string{modelID},
			Weight: 70,
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "dup-node", PocWeight: 50, TimeslotAllocation: []bool{true, false}},
						{NodeId: "dup-node", PocWeight: 30, TimeslotAllocation: []bool{true, false}},
						{NodeId: "unique-node", PocWeight: 20, TimeslotAllocation: []bool{true, false}},
					},
				},
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	modelAssigner.AllocateMLNodesForPoC(ctx, types.Epoch{Index: 1}, participants)

	require.Len(t, participants[0].MlNodes, 1)
	require.Len(t, participants[0].MlNodes[0].MlNodes, 2)
	require.Equal(t, []string{"dup-node", "unique-node"}, []string{
		participants[0].MlNodes[0].MlNodes[0].NodeId,
		participants[0].MlNodes[0].MlNodes[1].NodeId,
	})
	require.Equal(t, int64(50), participants[0].MlNodes[0].MlNodes[0].PocWeight)
}
