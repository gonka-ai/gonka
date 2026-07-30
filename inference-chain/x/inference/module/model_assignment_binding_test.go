package inference

import (
	"context"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const (
	bindingModelLarge = "Qwen/QwQ-32B"
	bindingModelSmall = "Qwen/Qwen2.5-7B-Instruct"
)

func bindingGovernanceModels() []types.Model {
	return []types.Model{
		{ProposedBy: "genesis", Id: bindingModelLarge, VRam: 32, ThroughputPerNonce: 1000},
		{ProposedBy: "genesis", Id: bindingModelSmall, VRam: 16, ThroughputPerNonce: 10000},
	}
}

func TestSetModelsForParticipants_HardwareCannotRelabelValidatedModel(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1relabelparticipant0000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: bindingGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "node-1", Models: []string{bindingModelLarge, bindingModelSmall}},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 100}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

	p := participants[0]
	require.Equal(t, []string{bindingModelSmall}, p.Models,
		"weight must stay in the model that was actually proved, even though the node declares the larger model first")
	require.Len(t, p.MlNodes, 1)
	require.Len(t, p.MlNodes[0].MlNodes, 1)
	require.Equal(t, "node-1", p.MlNodes[0].MlNodes[0].NodeId)
	require.Equal(t, int64(100), p.MlNodes[0].MlNodes[0].PocWeight)
	require.Equal(t, []bool{true, false}, p.MlNodes[0].MlNodes[0].TimeslotAllocation)
}

func TestSetModelsForParticipants_HardwareVetoDropsNodeInsteadOfMoving(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1vetoparticipant000000000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: bindingGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "node-1", Models: []string{bindingModelLarge}},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 100}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

	p := participants[0]
	require.Empty(t, p.Models, "node no longer declares the model it proved, so nothing survives")
	require.Empty(t, p.MlNodes)
}

func TestSetModelsForParticipants_NodeMissingFromHardwareIsDropped(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1missingnodeparticipant00000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: bindingGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "node-1", Models: []string{bindingModelSmall}},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{
					{NodeId: "node-1", PocWeight: 100},
					{NodeId: "ghost-node", PocWeight: 900},
				}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

	p := participants[0]
	require.Equal(t, []string{bindingModelSmall}, p.Models)
	require.Len(t, p.MlNodes[0].MlNodes, 1)
	require.Equal(t, "node-1", p.MlNodes[0].MlNodes[0].NodeId)
}

func TestSetModelsForParticipants_MultiModelKeepsPerNodeBinding(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1multimodelparticipant0000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: bindingGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "node-large", Models: []string{bindingModelLarge, bindingModelSmall}},
					{LocalId: "node-small", Models: []string{bindingModelLarge, bindingModelSmall}},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{bindingModelLarge, bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-large", PocWeight: 40}}},
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-small", PocWeight: 60}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

	p := participants[0]
	require.Equal(t, []string{bindingModelLarge, bindingModelSmall}, p.Models)
	require.Len(t, p.MlNodes, 2)
	require.Len(t, p.MlNodes[0].MlNodes, 1)
	require.Equal(t, "node-large", p.MlNodes[0].MlNodes[0].NodeId)
	require.Len(t, p.MlNodes[1].MlNodes, 1)
	require.Equal(t, "node-small", p.MlNodes[1].MlNodes[0].NodeId)
}

func TestSetModelsForParticipants_ContestedNodeResolvesToOneModel(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1contestedparticipant000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: bindingGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "node-1", Models: []string{bindingModelLarge, bindingModelSmall}},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{bindingModelLarge, bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 10}}},
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 70}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

	p := participants[0]
	require.Equal(t, []string{bindingModelSmall}, p.Models, "higher-weight claim wins the node")
	require.Len(t, p.MlNodes, 1)
	require.Len(t, p.MlNodes[0].MlNodes, 1)
	require.Equal(t, int64(70), p.MlNodes[0].MlNodes[0].PocWeight)
}

func TestSetModelsForParticipants_PreservedBindingSurvives(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1preservedparticipant000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: bindingGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "inference-node", Models: []string{bindingModelSmall}, Status: types.HardwareNodeStatus_INFERENCE},
				},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "inference-node", PocWeight: 55}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

	p := participants[0]
	require.Equal(t, []string{bindingModelSmall}, p.Models)
	require.Len(t, p.MlNodes, 1)
	require.Equal(t, int64(55), p.MlNodes[0].MlNodes[0].PocWeight)
}

func TestSetModelsForParticipants_RelabelDoesNotReachTargetSubgroup(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1subgroupparticipant0000000000000000000000"
	epoch := types.Epoch{Index: 1}

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: bindingGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			participantAddress: {
				Participant: participantAddress,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "node-1", Models: []string{bindingModelLarge, bindingModelSmall}},
				},
			},
		},
		perfSummaries: map[string]map[uint64]types.EpochPerformanceSummary{
			participantAddress: {0: {ParticipantId: participantAddress, EpochIndex: 0, RewardedCoins: 1}},
		},
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 100}}},
			},
		},
	}

	assigner := NewModelAssigner(mockKeeper, mockLogger{})
	assigner.setModelsForParticipants(ctx, participants, epoch)
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)

	smallGroup, foundSmall := mockKeeper.GetEpochGroupData(ctx, epoch.Index, bindingModelSmall)
	require.True(t, foundSmall, "participant belongs to the model it proved")
	require.Len(t, smallGroup.ValidationWeights, 1)
	require.Equal(t, participantAddress, smallGroup.ValidationWeights[0].MemberAddress)
	require.Equal(t, int64(100), smallGroup.ValidationWeights[0].MlNodes[0].PocWeight)

	largeGroup, foundLarge := mockKeeper.GetEpochGroupData(ctx, epoch.Index, bindingModelLarge)
	require.False(t, foundLarge && len(largeGroup.ValidationWeights) > 0,
		"participant must not appear in the subgroup of a model it never proved")

	snapshot, err := assigner.SamplePreservedForEpisode(ctx, epoch, 777)
	require.NoError(t, err)
	for _, modelNodes := range snapshot.ModelPreservedNodes {
		if modelNodes.ModelId != bindingModelLarge {
			continue
		}
		for _, p := range modelNodes.Participants {
			require.NotEqual(t, participantAddress, p.ParticipantId,
				"relabeled weight must not be preserved into the target model")
		}
	}
}

func TestSetModelsForParticipants_RelabelDoesNotInflateConfirmationWeight(t *testing.T) {
	coefficients := types.ConfirmationWeightCoefficients([]*types.ConfirmationWeightScale{
		{ModelId: bindingModelSmall, WeightScaleFactor: types.DecimalFromFloat(1.0)},
		{ModelId: bindingModelLarge, WeightScaleFactor: types.DecimalFromFloat(3.0)},
	})

	newParticipant := func(addr string) *types.ActiveParticipant {
		return &types.ActiveParticipant{
			Index:  addr,
			Models: []string{bindingModelSmall},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 100}}},
			},
		}
	}

	testCases := []struct {
		name             string
		declaredModels   []string
		expectedModels   []string
		expectedConfWeig int64
	}{
		{
			name:             "declares only the unproved model",
			declaredModels:   []string{bindingModelLarge},
			expectedModels:   nil,
			expectedConfWeig: 0,
		},
		{
			name:             "declares both models",
			declaredModels:   []string{bindingModelLarge, bindingModelSmall},
			expectedModels:   []string{bindingModelSmall},
			expectedConfWeig: 100,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			participantAddress := "gonka1confweightparticipant00000000000000000"

			mockKeeper := &mockKeeperForModelAssigner{
				governanceModels: bindingGovernanceModels(),
				hardwareNodes: map[string]*types.HardwareNodes{
					participantAddress: {
						Participant:   participantAddress,
						HardwareNodes: []*types.HardwareNode{{LocalId: "node-1", Models: tc.declaredModels}},
					},
				},
			}

			participants := []*types.ActiveParticipant{newParticipant(participantAddress)}
			require.Equal(t, int64(100),
				types.ConfirmationWeightOfParticipantWithCoefficients(participants[0], coefficients),
				"confirmation weight before assignment")

			NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 1})

			require.Equal(t, tc.expectedModels, participants[0].Models)
			require.Equal(t, tc.expectedConfWeig,
				types.ConfirmationWeightOfParticipantWithCoefficients(participants[0], coefficients),
				"relabeling must not apply the target model's coefficient")
		})
	}
}

func TestValidatedModelNodes_IgnoresUnpairedModels(t *testing.T) {
	p := &types.ActiveParticipant{
		Models: []string{bindingModelLarge, bindingModelSmall, ""},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{NodeId: "a", PocWeight: 1}, {NodeId: "", PocWeight: 9}, nil}},
		},
	}

	validated := validatedModelNodes(p)
	require.Len(t, validated, 1)
	require.Len(t, validated[bindingModelLarge], 1)
	require.Equal(t, "a", validated[bindingModelLarge][0].NodeId)
}

func TestResolveNodeOwnerModel_PrefersHighestWeightAndReportsContested(t *testing.T) {
	validated := map[string][]*types.MLNodeInfo{
		bindingModelLarge: {{NodeId: "shared", PocWeight: 5}, {NodeId: "solo", PocWeight: 3}},
		bindingModelSmall: {{NodeId: "shared", PocWeight: 50}},
	}

	owner, contested := resolveNodeOwnerModel(validated)

	require.Equal(t, []string{"shared"}, contested)
	require.Equal(t, bindingModelSmall, owner["shared"].modelId)
	require.Equal(t, int64(50), owner["shared"].node.PocWeight)
	require.Equal(t, bindingModelLarge, owner["solo"].modelId)
}

func TestResolveNodeOwnerModel_WithinModelDuplicateIsNotContested(t *testing.T) {
	validated := map[string][]*types.MLNodeInfo{
		bindingModelLarge: {{NodeId: "dup", PocWeight: 10}, {NodeId: "dup", PocWeight: 25}},
	}

	owner, contested := resolveNodeOwnerModel(validated)

	require.Empty(t, contested)
	require.Len(t, owner, 1)
	require.Equal(t, int64(25), owner["dup"].node.PocWeight)
}
