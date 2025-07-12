package inference

import (
	"context"
	keeper "github.com/productscience/inference/x/inference/keeper"
	"testing"

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

	require.Len(t, participant.MlNodes, 2, "Should have two MLNode groups due to the bug (one for model, one for overflow)")

	// Check first group (assigned model)
	modelGroup := participant.MlNodes[0]
	require.Len(t, modelGroup.MlNodes, 1, "The model-specific group should have one node")
	require.Equal(t, "mlnode1", modelGroup.MlNodes[0].NodeId, "The first node should be assigned to the model")
	// The node should be set to PRE_POC by default, not POC
	require.Equal(t, []bool{true, false}, modelGroup.MlNodes[0].TimeslotAllocation, "Timeslot allocation should be default [true, false]")

	// Check second group (overflow)
	overflowGroup := participant.MlNodes[1]
	require.Len(t, overflowGroup.MlNodes, 1, "The overflow group should have one node")
	require.Equal(t, "mlnode2", overflowGroup.MlNodes[0].NodeId, "The second node should be in the overflow group")
	require.Equal(t, []bool{true, false}, overflowGroup.MlNodes[0].TimeslotAllocation, "Timeslot allocation should be default [true, false]")
}
