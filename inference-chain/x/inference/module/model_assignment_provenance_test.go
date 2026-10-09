package inference

// Tests for the provenance-preserving seating rules in setModelsForParticipants:
//
//   - weight keeps its validated model; coefficients follow that model only
//   - hardware may exclude a node, never migrate it to another model
//   - hardware claims alone never create weight for a model
//   - hardware compatibility is filtered BEFORE cross-model selection, so an
//     incompatible claim can never shadow a compatible one
//   - single-assignment: one NodeId seats in at most one validated bucket;
//     several compatible claims -> the node is dropped as ambiguous
//   - bootstrap (no hardware record) skips only the hardware filter, never the
//     single-assignment rule
//   - preserved-carry participants obey the same rules (same code path)
//   - missing-inventory behavior preserved (no record / empty / unknown node /
//     known node without compatible model)
//   - unregistered alias node ids cannot reach weight consumers

import (
	"context"
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

const (
	// Governance order intentionally puts the high-coefficient "target" model first:
	// under the old first-supported-model assignment it would have captured the node.
	provTargetModel = "moonshotai/Kimi-K2" // high coefficient, attacker's target
	provSourceModel = "zai-org/MiniMax-M1" // model the weight was actually proven under
)

func provGovernanceModels() []types.Model {
	return []types.Model{
		{ProposedBy: "genesis", Id: provTargetModel, VRam: 96},
		{ProposedBy: "genesis", Id: provSourceModel, VRam: 64},
	}
}

func provCoefficients() map[string]mathsdk.LegacyDec {
	return map[string]mathsdk.LegacyDec{
		provTargetModel: mathsdk.LegacyMustNewDecFromStr("0.945"),
		provSourceModel: mathsdk.LegacyMustNewDecFromStr("0.3024"),
	}
}

// participantWithValidated builds a participant whose node proved provSourceModel,
// the shape ComputeNewWeights produces for both fresh PoC and preserved carry.
func participantWithValidated(addr string, nodeId string, weight int64) *types.ActiveParticipant {
	return &types.ActiveParticipant{
		Index:  addr,
		Models: []string{provSourceModel},
		MlNodes: []*types.ModelMLNodes{
			{MlNodes: []*types.MLNodeInfo{{NodeId: nodeId, PocWeight: weight}}},
		},
	}
}

func provKeeper(addr string, declared ...string) *mockKeeperForModelAssigner {
	return &mockKeeperForModelAssigner{
		governanceModels: provGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			addr: {
				Participant: addr,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "node-1", Models: declared},
				},
			},
		},
	}
}

// Hardware adding the target model must not move the node out of its
// validated bucket, even though the target model comes first in governance order.
func TestSetModelsForParticipants_HardwareCannotRelabelValidatedWeight(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1relabel0000000000000000000000000000000"

	mockKeeper := provKeeper(addr, provTargetModel, provSourceModel)
	participants := []*types.ActiveParticipant{participantWithValidated(addr, "node-1", 1000)}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Equal(t, []string{provSourceModel}, p.Models, "node must stay in the model it proved")
	require.Len(t, p.MlNodes, 1)
	require.Equal(t, "node-1", p.MlNodes[0].MlNodes[0].NodeId)
	require.Equal(t, int64(1000), p.MlNodes[0].MlNodes[0].PocWeight)

	// Coefficient / subgroup math must follow the validated model only.
	groups := buildGroupData(participants, provCoefficients(), provSourceModel, mockLogger{})
	require.Contains(t, groups, provSourceModel)
	require.Equal(t, int64(1000), groups[provSourceModel].MemberPocWeights[addr])
	require.NotContains(t, groups, provTargetModel, "no subgroup may be created from a hardware claim")
	require.True(t, groups[provSourceModel].ConsensusKoeff.Equal(provCoefficients()[provSourceModel]))
}

// Hardware no longer declares the validated model -> the node is dropped for
// seating, not migrated to the still-declared target model.
func TestSetModelsForParticipants_HardwareDropOfValidatedModelExcludesNode(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1drop000000000000000000000000000000000"

	mockKeeper := provKeeper(addr, provTargetModel) // source model no longer declared
	participants := []*types.ActiveParticipant{participantWithValidated(addr, "node-1", 1000)}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Empty(t, p.Models, "dropped node must not resurface under another model")
	require.Empty(t, p.MlNodes)
	require.Equal(t, int64(0), RecalculateWeight(p))

	groups := buildGroupData(participants, provCoefficients(), provSourceModel, mockLogger{})
	require.Empty(t, groups, "no subgroup weight may survive the drop")
}

// Declaring a model on hardware creates no weight when nothing was proven for it.
func TestSetModelsForParticipants_HardwareClaimAloneCreatesNoWeight(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1noproof0000000000000000000000000000000"

	mockKeeper := provKeeper(addr, provTargetModel, provSourceModel)
	// Participant proved nothing (no validated buckets at all).
	participants := []*types.ActiveParticipant{{Index: addr}}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Empty(t, p.Models)
	require.Empty(t, p.MlNodes)
	require.Equal(t, int64(0), RecalculateWeight(p))
}

// F2: a contested node keeps the claim its hardware actually declares, even when
// an incompatible claim has higher raw weight. The compatibility filter runs
// before cross-model selection, so the incompatible claim cannot shadow it.
func TestSetModelsForParticipants_OnlyCompatibleContestedClaimSurvives(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1compat000000000000000000000000000000000"

	// Hardware declares only the target model; the higher-weight source claim is
	// incompatible and must not knock out the compatible target claim.
	mockKeeper := provKeeper(addr, provTargetModel)
	participants := []*types.ActiveParticipant{
		{
			Index:  addr,
			Models: []string{provSourceModel, provTargetModel},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 100}}},
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 90}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Equal(t, []string{provTargetModel}, p.Models, "only the hardware-compatible validated claim survives")
	require.Len(t, p.MlNodes, 1)
	require.Len(t, p.MlNodes[0].MlNodes, 1)
	require.Equal(t, int64(90), p.MlNodes[0].MlNodes[0].PocWeight)
	require.Equal(t, int64(90), RecalculateWeight(p), "incompatible claim must not contribute")

	groups := buildGroupData(participants, provCoefficients(), provSourceModel, mockLogger{})
	require.NotContains(t, groups, provSourceModel, "incompatible claim must not reach a subgroup")
	require.Equal(t, int64(90), groups[provTargetModel].MemberPocWeights[addr])
}

// A node validated under two models that BOTH pass the hardware filter is
// dropped as ambiguous: one ML node serves one model, and no selection rule is
// invented for inconsistent input.
func TestSetModelsForParticipants_MultipleCompatibleClaimsAreRejectedAsAmbiguous(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1ambiguous00000000000000000000000000000"

	mockKeeper := provKeeper(addr, provTargetModel, provSourceModel)
	participants := []*types.ActiveParticipant{
		{
			Index:  addr,
			Models: []string{provTargetModel, provSourceModel},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 10}}},
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 70}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Empty(t, p.Models, "ambiguous node must not seat anywhere")
	require.Empty(t, p.MlNodes)
	require.Equal(t, int64(0), RecalculateWeight(p))

	groups := buildGroupData(participants, provCoefficients(), provSourceModel, mockLogger{})
	require.Empty(t, groups, "ambiguous claims must not reach any subgroup or coefficient")
}

// Within one model, duplicate entries for the same node id collapse to the
// highest-preference entry; this is dedup, not ambiguity.
func TestSetModelsForParticipants_WithinModelDuplicateUsesDeterministicPreference(t *testing.T) {
	validated := map[string][]*types.MLNodeInfo{
		"model-a": {{NodeId: "dup", PocWeight: 10}, {NodeId: "dup", PocWeight: 25}},
	}

	owners, ambiguous, incompatible := resolveUniqueCompatibleClaims(validated, nil, false)

	require.Empty(t, ambiguous)
	require.Empty(t, incompatible)
	require.Len(t, owners, 1)
	require.Equal(t, int64(25), owners["dup"].node.PocWeight)
}

// Slot allocations carried over from a prior epoch (preserved carry / fallback)
// are reset before any preference comparison, so stale slot state can neither
// influence within-model dedup nor leak into the seated result.
func TestSetModelsForParticipants_StaleTimeslotAllocationIsReset(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1staleslots00000000000000000000000000000"

	mockKeeper := provKeeper(addr, provSourceModel)
	participants := []*types.ActiveParticipant{
		{
			Index:  addr,
			Models: []string{provSourceModel},
			MlNodes: []*types.ModelMLNodes{
				// The stale-slot entry would win the raw preference comparison on a
				// weight tie if allocations were compared un-reset.
				{MlNodes: []*types.MLNodeInfo{
					{NodeId: "node-1", PocWeight: 50},
					{NodeId: "node-1", PocWeight: 50, TimeslotAllocation: []bool{true, true}},
				}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Equal(t, []string{provSourceModel}, p.Models)
	require.Len(t, p.MlNodes, 1)
	require.Len(t, p.MlNodes[0].MlNodes, 1)
	require.Equal(t, []bool{true, false}, p.MlNodes[0].MlNodes[0].TimeslotAllocation,
		"seated node must carry the fresh upcoming-epoch allocation")
}

// A preserved-carry participant (same shape, weight earned in a prior epoch)
// goes through the identical filter: kept while hardware declares the model,
// dropped (not relabeled) when it does not.
func TestSetModelsForParticipants_PreservedCarryObeysSameRules(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1preserved000000000000000000000000000000"

	t.Run("hardware still declares the preserved model", func(t *testing.T) {
		mockKeeper := provKeeper(addr, provSourceModel)
		participants := []*types.ActiveParticipant{participantWithValidated(addr, "node-1", 55)}

		NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

		p := participants[0]
		require.Equal(t, []string{provSourceModel}, p.Models)
		require.Equal(t, int64(55), p.MlNodes[0].MlNodes[0].PocWeight)
	})

	t.Run("hardware relabeled to the target model", func(t *testing.T) {
		mockKeeper := provKeeper(addr, provTargetModel)
		participants := []*types.ActiveParticipant{participantWithValidated(addr, "node-1", 55)}

		NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

		p := participants[0]
		require.Empty(t, p.Models, "preserved weight must not follow the hardware relabel")
		require.Empty(t, p.MlNodes)
	})
}

// No HardwareNodes record at all (bootstrap) keeps the validated assignments
// untouched and only initializes timeslot allocations.
func TestSetModelsForParticipants_NoHardwareRecordKeepsValidatedAssignments(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1bootstrap000000000000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: provGovernanceModels(),
		hardwareNodes:    map[string]*types.HardwareNodes{}, // no record for addr
	}
	participants := []*types.ActiveParticipant{participantWithValidated(addr, "node-1", 1000)}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Equal(t, []string{provSourceModel}, p.Models)
	require.Len(t, p.MlNodes, 1)
	require.Equal(t, int64(1000), p.MlNodes[0].MlNodes[0].PocWeight)
	require.Equal(t, []bool{true, false}, p.MlNodes[0].MlNodes[0].TimeslotAllocation)
}

// Bootstrap (no hardware record) skips the hardware filter but not the
// single-assignment rule: a node id validated under two models is dropped as
// ambiguous even when there is no inventory to filter against.
func TestSetModelsForParticipants_NoHardwareStillRejectsAmbiguousNodeId(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1bootambig00000000000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: provGovernanceModels(),
		hardwareNodes:    map[string]*types.HardwareNodes{}, // no record for addr
	}
	participants := []*types.ActiveParticipant{
		{
			Index:  addr,
			Models: []string{provTargetModel, provSourceModel},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 40}}},
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 60}}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Empty(t, p.Models, "bootstrap must not bypass single assignment")
	require.Empty(t, p.MlNodes)
	require.Equal(t, int64(0), RecalculateWeight(p))
}

// Bootstrap participants must not have one node id counted under several model
// coefficients: the double-counting is checked all the way through group data,
// not only on the intermediate arrays.
func TestSetModelsForParticipants_NoHardwareCannotCountNodeAcrossCoefficients(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1bootdouble0000000000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: provGovernanceModels(),
		hardwareNodes:    map[string]*types.HardwareNodes{}, // no record for addr
	}
	participants := []*types.ActiveParticipant{
		{
			Index: addr,
			// "double" appears under both models (would be counted twice, once per
			// coefficient, if the bootstrap branch skipped normalization);
			// "honest" is a normal unique claim that must survive.
			Models: []string{provTargetModel, provSourceModel},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{{NodeId: "double", PocWeight: 1_000}}},
				{MlNodes: []*types.MLNodeInfo{
					{NodeId: "double", PocWeight: 1_000},
					{NodeId: "honest", PocWeight: 100},
				}},
			},
		},
	}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Equal(t, []string{provSourceModel}, p.Models)
	require.Len(t, p.MlNodes, 1)
	require.Len(t, p.MlNodes[0].MlNodes, 1, "only the unique claim survives")
	require.Equal(t, "honest", p.MlNodes[0].MlNodes[0].NodeId)
	require.Equal(t, int64(100), RecalculateWeight(p), "ambiguous node must not be counted at all, let alone twice")

	groups := buildGroupData(participants, provCoefficients(), provSourceModel, mockLogger{})
	require.NotContains(t, groups, provTargetModel, "no target-model subgroup may exist")
	require.Equal(t, int64(100), groups[provSourceModel].MemberPocWeights[addr],
		"subgroup weight must reflect only the unique claim")
}

// An existing but empty inventory excludes every node from seating.
func TestSetModelsForParticipants_EmptyInventoryExcludesAllNodes(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1empty000000000000000000000000000000000"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: provGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			addr: {Participant: addr, HardwareNodes: []*types.HardwareNode{}},
		},
	}
	participants := []*types.ActiveParticipant{participantWithValidated(addr, "node-1", 1000)}

	NewModelAssigner(mockKeeper, mockLogger{}).setModelsForParticipants(ctx, participants, types.Epoch{Index: 2})

	p := participants[0]
	require.Empty(t, p.Models)
	require.Empty(t, p.MlNodes)
	require.Equal(t, int64(0), RecalculateWeight(p))
}

// An intermediate ComputeNewWeights result carrying an unregistered alias
// node id loses the alias before any weight consumer; the registered node's
// weight is unaffected.
func TestSetModelsForParticipants_UnregisteredAliasRemovedBeforeWeightConsumers(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1alias000000000000000000000000000000000"
	epoch := types.Epoch{Index: 1}

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: provGovernanceModels(),
		hardwareNodes: map[string]*types.HardwareNodes{
			addr: {
				Participant: addr,
				HardwareNodes: []*types.HardwareNode{
					{LocalId: "real-node", Models: []string{provSourceModel}},
				},
			},
		},
		perfSummaries: map[string]map[uint64]types.EpochPerformanceSummary{
			addr: {0: {ParticipantId: addr, EpochIndex: 0, RewardedCoins: 1}},
		},
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1},
			},
		},
	}

	participants := []*types.ActiveParticipant{
		{
			Index:  addr,
			Models: []string{provSourceModel},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: []*types.MLNodeInfo{
					{NodeId: "real-node", PocWeight: 100},
					{NodeId: "alias-node", PocWeight: 1_000_000}, // not in inventory
				}},
			},
		},
	}

	assigner := NewModelAssigner(mockKeeper, mockLogger{})
	assigner.setModelsForParticipants(ctx, participants, epoch)

	p := participants[0]
	require.Equal(t, []string{provSourceModel}, p.Models)
	require.Len(t, p.MlNodes, 1)
	require.Len(t, p.MlNodes[0].MlNodes, 1, "alias must be removed")
	require.Equal(t, "real-node", p.MlNodes[0].MlNodes[0].NodeId)

	// Root weight, subgroup weight, and coefficient math see only the real node.
	require.Equal(t, int64(100), RecalculateWeight(p))
	groups := buildGroupData(participants, provCoefficients(), provSourceModel, mockLogger{})
	require.Equal(t, int64(100), groups[provSourceModel].MemberPocWeights[addr])

	// The preserved-episode sampler (downstream consumer) never sees the alias.
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)
	snapshot, err := assigner.SamplePreservedForEpisode(ctx, epoch, 777)
	require.NoError(t, err)
	for _, modelNodes := range snapshot.ModelPreservedNodes {
		for _, pp := range modelNodes.Participants {
			require.NotContains(t, pp.NodeIds, "alias-node")
		}
	}
}

// Smoke: after provenance-preserving seating, the target-model subgroup never
// materializes for a relabeling participant, so preserved sampling for that model
// cannot carry the weight either.
func TestSetModelsForParticipants_RelabelDoesNotReachTargetSubgroup(t *testing.T) {
	ctx := context.Background()
	addr := "gonka1subgroup00000000000000000000000000000"
	epoch := types.Epoch{Index: 1}

	mockKeeper := provKeeper(addr, provTargetModel, provSourceModel)
	mockKeeper.perfSummaries = map[string]map[uint64]types.EpochPerformanceSummary{
		addr: {0: {ParticipantId: addr, EpochIndex: 0, RewardedCoins: 1}},
	}
	mockKeeper.params = &types.Params{
		EpochParams: &types.EpochParams{
			PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1},
		},
	}

	participants := []*types.ActiveParticipant{participantWithValidated(addr, "node-1", 100)}

	assigner := NewModelAssigner(mockKeeper, mockLogger{})
	assigner.setModelsForParticipants(ctx, participants, epoch)
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)

	sourceGroup, foundSource := mockKeeper.GetEpochGroupData(ctx, epoch.Index, provSourceModel)
	require.True(t, foundSource)
	require.Len(t, sourceGroup.ValidationWeights, 1)
	require.Equal(t, addr, sourceGroup.ValidationWeights[0].MemberAddress)
	require.Equal(t, int64(100), sourceGroup.ValidationWeights[0].MlNodes[0].PocWeight)

	targetGroup, foundTarget := mockKeeper.GetEpochGroupData(ctx, epoch.Index, provTargetModel)
	require.False(t, foundTarget && len(targetGroup.ValidationWeights) > 0,
		"participant must not appear in the subgroup of a model it never proved")

	snapshot, err := assigner.SamplePreservedForEpisode(ctx, epoch, 777)
	require.NoError(t, err)
	for _, modelNodes := range snapshot.ModelPreservedNodes {
		if modelNodes.ModelId != provTargetModel {
			continue
		}
		for _, pp := range modelNodes.Participants {
			require.NotEqual(t, addr, pp.ParticipantId,
				"relabeled weight must not be preserved into the target model")
		}
	}
}
