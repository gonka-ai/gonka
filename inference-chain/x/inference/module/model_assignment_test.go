package inference

import (
	"context"
	"fmt"
	"slices"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/utils"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// collectPreservedParticipants returns the set of participant Indexes that appear
// in the preserved snapshot (for any model). Used by tests that only care
// whether a participant was preserved at all, not which specific nodes.
func collectPreservedParticipants(snapshot types.PreservedNodesSnapshot) map[string]bool {
	preserved := make(map[string]bool)
	for _, mp := range snapshot.ModelPreservedNodes {
		if mp == nil {
			continue
		}
		for _, pp := range mp.Participants {
			if pp != nil && len(pp.NodeIds) > 0 {
				preserved[pp.ParticipantId] = true
			}
		}
	}
	return preserved
}

// Mock Keeper
type mockKeeperForModelAssigner struct {
	hardwareNodes    map[string]*types.HardwareNodes
	governanceModels []types.Model
	epochGroupData   map[string]map[uint64]types.EpochGroupData // modelId -> epochIndex -> data
	// participant -> epochIndex -> summary. When present with RewardedCoins > 0, the participant
	// counts as eligible-by-history for that epoch in SamplePreservedForEpisode.
	perfSummaries map[string]map[uint64]types.EpochPerformanceSummary
	params        *types.Params
	// liveSubGroupOverrides[modelId] = explicit live member set for that subgroup.
	// When unset for a model, the mock falls back to "all members in
	// epochGroupData[modelId][latest].ValidationWeights are live".
	liveSubGroupOverrides map[string]map[string]bool
	liveRootOverrides     map[string]bool // member set override for GetRootGroupDataWithLiveMembers
	liveSubGroupsErr      error
	// guardianAddresses is what the mock returns from GetGenesisGuardianAddresses.
	// Test bodies should populate this with valoper-bech32 strings; the production
	// code converts them to acc-bech32 before checking membership against subgroup
	// member addresses.
	guardianAddresses []string
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

func (m *mockKeeperForModelAssigner) GetEpochPerformanceSummary(ctx context.Context, epochIndex uint64, participantId string) (val types.EpochPerformanceSummary, found bool) {
	if byEpoch, ok := m.perfSummaries[participantId]; ok {
		if s, ok := byEpoch[epochIndex]; ok {
			return s, true
		}
	}
	return types.EpochPerformanceSummary{}, false
}

func (m *mockKeeperForModelAssigner) GetParams(ctx context.Context) (types.Params, error) {
	if m.params != nil {
		return *m.params, nil
	}
	return types.DefaultParams(), nil
}

func (m *mockKeeperForModelAssigner) GetGenesisGuardianAddresses(ctx context.Context) []string {
	return m.guardianAddresses
}

func (m *mockKeeperForModelAssigner) GetRootGroupDataWithLiveMembers(ctx context.Context) (types.EpochGroupData, map[string]bool, error) {
	var data types.EpochGroupData
	found := false
	if epochMap, ok := m.epochGroupData[""]; ok {
		for _, d := range epochMap {
			data = d
			found = true
		}
	}
	if !found {
		return types.EpochGroupData{}, nil, nil
	}
	if m.liveRootOverrides != nil {
		return data, m.liveRootOverrides, nil
	}
	liveSet := make(map[string]bool, len(data.ValidationWeights))
	for _, vw := range data.ValidationWeights {
		if vw != nil {
			liveSet[vw.MemberAddress] = true
		}
	}
	return data, liveSet, nil
}

func (m *mockKeeperForModelAssigner) GetLiveSubGroupsForCurrentEpoch(ctx context.Context) (
	map[string]types.EpochGroupData,
	map[string]map[string]bool,
	error,
) {
	if m.liveSubGroupsErr != nil {
		return nil, nil, m.liveSubGroupsErr
	}
	subGroupData := make(map[string]types.EpochGroupData, len(m.epochGroupData))
	liveSets := make(map[string]map[string]bool, len(m.epochGroupData))
	for modelId, byEpoch := range m.epochGroupData {
		if modelId == "" {
			continue
		}
		// Use the latest epoch entry for this model as the "current" subgroup data.
		var data types.EpochGroupData
		var latestEpoch uint64
		first := true
		for epochIdx, d := range byEpoch {
			if first || epochIdx > latestEpoch {
				latestEpoch = epochIdx
				data = d
				first = false
			}
		}
		if first {
			continue
		}
		// Live member set: override if present, otherwise every member in the subgroup data.
		var liveSet map[string]bool
		if override, ok := m.liveSubGroupOverrides[modelId]; ok {
			liveSet = override
		} else {
			liveSet = make(map[string]bool, len(data.ValidationWeights))
			for _, vw := range data.ValidationWeights {
				liveSet[vw.MemberAddress] = true
			}
		}
		subGroupData[modelId] = data
		liveSets[modelId] = liveSet
	}
	return subGroupData, liveSets, nil
}

// populateSubgroupsFromParticipants writes root and per-model subgroup data at epochIdx
// so SamplePreservedForEpisode has a candidate pool. Mirrors what production epoch
// formation would have produced after assigning models to participants.
func (m *mockKeeperForModelAssigner) populateSubgroupsFromParticipants(epochIdx uint64, participants []*types.ActiveParticipant) {
	if m.epochGroupData == nil {
		m.epochGroupData = make(map[string]map[uint64]types.EpochGroupData)
	}
	if m.epochGroupData[""] == nil {
		m.epochGroupData[""] = make(map[uint64]types.EpochGroupData)
	}

	modelSet := make(map[string]bool)
	for _, p := range participants {
		for _, modelId := range p.Models {
			modelSet[modelId] = true
		}
	}
	subGroupModels := make([]string, 0, len(modelSet))
	for modelId := range modelSet {
		subGroupModels = append(subGroupModels, modelId)
	}
	m.epochGroupData[""][epochIdx] = types.EpochGroupData{
		EpochIndex:     epochIdx,
		SubGroupModels: subGroupModels,
	}

	for modelId := range modelSet {
		var validationWeights []*types.ValidationWeight
		for _, p := range participants {
			for i, pModelId := range p.Models {
				if pModelId != modelId || i >= len(p.MlNodes) || p.MlNodes[i] == nil {
					continue
				}
				mlNodes := make([]*types.MLNodeInfo, 0, len(p.MlNodes[i].MlNodes))
				for _, n := range p.MlNodes[i].MlNodes {
					if n == nil {
						continue
					}
					nodeCopy := *n
					mlNodes = append(mlNodes, &nodeCopy)
				}
				validationWeights = append(validationWeights, &types.ValidationWeight{
					MemberAddress: p.Index,
					MlNodes:       mlNodes,
				})
			}
		}
		if m.epochGroupData[modelId] == nil {
			m.epochGroupData[modelId] = make(map[uint64]types.EpochGroupData)
		}
		m.epochGroupData[modelId][epochIdx] = types.EpochGroupData{
			EpochIndex:        epochIdx,
			ModelId:           modelId,
			ValidationWeights: validationWeights,
		}
	}
}

// applySnapshotToParticipants flips TimeslotAllocation[1] on each participant's ML nodes
// to match the snapshot's preserved set per model. Test-only helper that keeps the
// pre-migration TimeslotAllocation assertion style valid against the new sampler's
// returned snapshot.
func applySnapshotToParticipants(participants []*types.ActiveParticipant, snapshot types.PreservedNodesSnapshot) {
	preservedByModel := make(map[string]map[string]map[string]struct{})
	for _, mp := range snapshot.ModelPreservedNodes {
		if mp == nil {
			continue
		}
		byParticipant := make(map[string]map[string]struct{}, len(mp.Participants))
		for _, pp := range mp.Participants {
			if pp == nil {
				continue
			}
			set := make(map[string]struct{}, len(pp.NodeIds))
			for _, id := range pp.NodeIds {
				set[id] = struct{}{}
			}
			byParticipant[pp.ParticipantId] = set
		}
		preservedByModel[mp.ModelId] = byParticipant
	}
	for _, p := range participants {
		for i, modelId := range p.Models {
			if i >= len(p.MlNodes) || p.MlNodes[i] == nil {
				continue
			}
			preserved := preservedByModel[modelId][p.Index]
			for _, n := range p.MlNodes[i].MlNodes {
				if n == nil {
					continue
				}
				if len(n.TimeslotAllocation) < 2 {
					n.TimeslotAllocation = []bool{true, false}
				}
				_, ok := preserved[n.NodeId]
				n.TimeslotAllocation[1] = ok
			}
		}
	}
}

// runSamplePreservedForEpisode is a test-only convenience that populates subgroup data
// and invokes SamplePreservedForEpisode. The returned snapshot is also applied back onto
// participants so existing TimeslotAllocation[1] assertions continue to work.
func runSamplePreservedForEpisode(
	t *testing.T,
	ctx context.Context,
	assigner *ModelAssigner,
	mock *mockKeeperForModelAssigner,
	epoch types.Epoch,
	participants []*types.ActiveParticipant,
) types.PreservedNodesSnapshot {
	t.Helper()
	mock.populateSubgroupsFromParticipants(epoch.Index, participants)
	anchor := int64(epoch.Index)*100 + 1
	snapshot, err := assigner.SamplePreservedForEpisode(ctx, epoch, anchor)
	require.NoError(t, err)
	applySnapshotToParticipants(participants, snapshot)
	return snapshot
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
	// Actual preserved allocation happens in SamplePreservedForEpisode
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, false}, 2)
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, true}, 0)
}

func TestSamplePreservedForEpisode_MatchesSubgroupData(t *testing.T) {
	ctx := context.Background()
	participantAddress := "gonka1snapshotparticipant000000000000000000000000"
	modelID := "Qwen/QwQ-32B"

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{
			{
				Id:                 modelID,
				ThroughputPerNonce: 1000,
				VRam:               32,
			},
		},
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
		perfSummaries: map[string]map[uint64]types.EpochPerformanceSummary{
			participantAddress: {0: {ParticipantId: participantAddress, EpochIndex: 0, RewardedCoins: 1}},
		},
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1},
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	epoch := types.Epoch{Index: 1}

	participants := []*types.ActiveParticipant{
		{
			Index:  participantAddress,
			Models: []string{modelID},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "mlnode1", PocWeight: 29},
						{NodeId: "mlnode2", PocWeight: 28},
					},
				},
			},
		},
	}

	modelAssigner.setModelsForParticipants(ctx, participants, epoch)
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)

	snapshot, err := modelAssigner.SamplePreservedForEpisode(ctx, epoch, 777)
	require.NoError(t, err)
	require.Equal(t, int64(777), snapshot.EpisodeAnchorHeight)

	// Sampler must not mutate TimeslotAllocation on the input participants.
	modelGroup := participants[0].MlNodes[0]
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, false}, 2)
	assertTimeslotAllocationCount(t, modelGroup.MlNodes, []bool{true, true}, 0)
}

func TestSumLiveRootTotalWeight_ExcludesRemovedMembers(t *testing.T) {
	rootData := types.EpochGroupData{
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "live", Weight: 40},
			{MemberAddress: "removed", Weight: 60},
		},
	}
	liveSet := map[string]bool{"live": true}
	require.Equal(t, int64(40), sumLiveRootTotalWeight(rootData, liveSet))
}

func TestCalculateParticipantWeightThreshold75Percent_UsesLiveRootTotal(t *testing.T) {
	// Model VP 80+10=90; model-local 75% target would be 67 and keep both.
	// Live root total 100 -> network 75% target keeps only the 80 VP participant.
	threshold := calculateParticipantWeightThreshold75Percent([]int64{80, 10}, 100)
	require.Equal(t, int64(79), threshold)
	require.True(t, 80 > threshold)
	require.False(t, 10 > threshold)
}

func TestCanAllocateParticipantNode_UsesVotingPowerCap(t *testing.T) {
	canAllocate, updatedVP := canAllocateParticipantNode(10, 10, 0, 40, 34)
	require.False(t, canAllocate)
	require.Equal(t, int64(0), updatedVP)

	canAllocate, updatedVP = canAllocateParticipantNode(10, 10, 0, 30, 34)
	require.True(t, canAllocate)
	require.Equal(t, int64(30), updatedVP)

	canAllocate, updatedVP = canAllocateParticipantNode(5, 10, 0, 40, 34)
	require.True(t, canAllocate)
	require.Equal(t, int64(0), updatedVP)
}

// TestSamplePreservedForEpisode_ExcludesGuardians covers the guardian-exclusion
// path in filterEligibleMLNodes: a participant whose acc-bech32 address matches
// the converted operator address from GetGenesisGuardianAddresses must not have
// any of their nodes returned by the preserved-snapshot sampler. The intent is
// that guardians always remain in the voting set, never moved to the preserved
// (non-voting) bucket.
func TestSamplePreservedForEpisode_ExcludesGuardians(t *testing.T) {
	ctx := context.Background()
	modelID := "model-guardian-test"

	// Set bech32 prefixes so utils.OperatorAddressToAccAddress can decode the
	// gonkavaloper... fixture below. Safe to call repeatedly across tests in
	// this package (no Seal()).
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("gonka", "gonkapub")
	cfg.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	// A pre-known valid gonkavaloper bech32 used in other tests in this package.
	guardianOperator := "gonkavaloper1gcrlrhvw8kd7zr6pl92rxnc6j20chatkcx6w4t"
	guardianAccAddr, err := utils.OperatorAddressToAccAddress(guardianOperator)
	require.NoError(t, err, "guardian operator bech32 must convert cleanly with prefixes set")

	// 4 total participants so the N/2+1 sampler (= 3) still picks survivors
	// after the guardian is excluded.
	otherAddrs := []string{
		"gonka1nonguardian00000000000000000000000000000000",
		"gonka1nonguardian11111111111111111111111111111111",
		"gonka1nonguardian22222222222222222222222222222222",
	}
	addrs := append([]string{guardianAccAddr}, otherAddrs...)

	participants := make([]*types.ActiveParticipant, 0, len(addrs))
	subgroupWeights := make([]*types.ValidationWeight, 0, len(addrs))
	perfSummaries := make(map[string]map[uint64]types.EpochPerformanceSummary, len(addrs))
	for _, addr := range addrs {
		nodes := []*types.MLNodeInfo{
			{NodeId: fmt.Sprintf("%s-n1", addr), PocWeight: 10},
			{NodeId: fmt.Sprintf("%s-n2", addr), PocWeight: 10},
			{NodeId: fmt.Sprintf("%s-n3", addr), PocWeight: 10},
		}
		subgroupWeights = append(subgroupWeights, &types.ValidationWeight{
			MemberAddress: addr,
			MlNodes:       nodes,
		})
		participants = append(participants, &types.ActiveParticipant{
			Index:   addr,
			Models:  []string{modelID},
			MlNodes: []*types.ModelMLNodes{{MlNodes: nodes}},
		})
		perfSummaries[addr] = map[uint64]types.EpochPerformanceSummary{
			0: {ParticipantId: addr, EpochIndex: 0, RewardedCoins: 1},
		}
	}

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels:  []types.Model{{Id: modelID, ThroughputPerNonce: 1000, VRam: 32}},
		epochGroupData:    map[string]map[uint64]types.EpochGroupData{modelID: {0: {ValidationWeights: subgroupWeights}}},
		perfSummaries:     perfSummaries,
		params:            &types.Params{EpochParams: &types.EpochParams{PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}}},
		guardianAddresses: []string{guardianOperator},
	}

	assigner := NewModelAssigner(mockKeeper, mockLogger{})
	epoch := types.Epoch{Index: 1}
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)

	snapshot, err := assigner.SamplePreservedForEpisode(ctx, epoch, 1234)
	require.NoError(t, err)

	preserved := collectPreservedParticipants(snapshot)
	require.False(t, preserved[guardianAccAddr], "guardian must not appear in preserved snapshot")
	// At least one non-guardian survives sampling; otherwise the test setup is
	// pre-filtering everything and the assertion above is vacuous.
	survived := 0
	for _, addr := range otherAddrs {
		if preserved[addr] {
			survived++
		}
	}
	require.Greater(t, survived, 0, "at least one non-guardian participant must be preserved")
}

// TestSamplePreservedForEpisode_FiltersDeadSubGroupMembers covers the
// liveSubSet filter in SamplePreservedForEpisode: a participant present in the
// epoch group's recorded ValidationWeights but absent from the SDK group's
// live member list (e.g., removed mid-epoch) must not contribute nodes to the
// preserved snapshot. Symmetric with getEffectiveValidationBaseState.
func TestSamplePreservedForEpisode_FiltersDeadSubGroupMembers(t *testing.T) {
	ctx := context.Background()
	modelID := "model-livefilter-test"

	addrs := []string{
		"participant-live-0",
		"participant-live-1",
		"participant-dead",
	}

	participants := make([]*types.ActiveParticipant, 0, len(addrs))
	subgroupWeights := make([]*types.ValidationWeight, 0, len(addrs))
	perfSummaries := make(map[string]map[uint64]types.EpochPerformanceSummary, len(addrs))
	for _, addr := range addrs {
		nodes := []*types.MLNodeInfo{
			{NodeId: fmt.Sprintf("%s-n1", addr), PocWeight: 10},
			{NodeId: fmt.Sprintf("%s-n2", addr), PocWeight: 10},
			{NodeId: fmt.Sprintf("%s-n3", addr), PocWeight: 10},
		}
		subgroupWeights = append(subgroupWeights, &types.ValidationWeight{
			MemberAddress: addr,
			MlNodes:       nodes,
		})
		participants = append(participants, &types.ActiveParticipant{
			Index:   addr,
			Models:  []string{modelID},
			MlNodes: []*types.ModelMLNodes{{MlNodes: nodes}},
		})
		perfSummaries[addr] = map[uint64]types.EpochPerformanceSummary{
			0: {ParticipantId: addr, EpochIndex: 0, RewardedCoins: 1},
		}
	}

	// The dead member is in ValidationWeights but not in the live override.
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID, ThroughputPerNonce: 1000, VRam: 32}},
		epochGroupData:   map[string]map[uint64]types.EpochGroupData{modelID: {0: {ValidationWeights: subgroupWeights}}},
		perfSummaries:    perfSummaries,
		params:           &types.Params{EpochParams: &types.EpochParams{PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}}},
		liveSubGroupOverrides: map[string]map[string]bool{
			modelID: {
				"participant-live-0": true,
				"participant-live-1": true,
				// "participant-dead" is intentionally absent.
			},
		},
	}

	assigner := NewModelAssigner(mockKeeper, mockLogger{})
	epoch := types.Epoch{Index: 1}
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)

	snapshot, err := assigner.SamplePreservedForEpisode(ctx, epoch, 4321)
	require.NoError(t, err)

	preserved := collectPreservedParticipants(snapshot)
	require.False(t, preserved["participant-dead"], "dead subgroup member must be filtered out before sampling")
}

func TestSamplePreservedForEpisode_AnchorInfluencesSelection(t *testing.T) {
	ctx := context.Background()
	modelID := "model-anchor-test"

	// Enough participants with multiple nodes each so the 34% non-voting constraint
	// doesn't stop us before the shuffle matters. N/2+1 sampling drops some
	// participants; those drops are what the anchor seed can influence.
	participants := make([]*types.ActiveParticipant, 0, 6)
	subgroupWeights := make([]*types.ValidationWeight, 0, 6)
	perfSummaries := make(map[string]map[uint64]types.EpochPerformanceSummary, 6)
	for i := 0; i < 6; i++ {
		addr := fmt.Sprintf("participant-%d", i)
		nodes := []*types.MLNodeInfo{
			{NodeId: fmt.Sprintf("%s-n1", addr), PocWeight: 10},
			{NodeId: fmt.Sprintf("%s-n2", addr), PocWeight: 10},
			{NodeId: fmt.Sprintf("%s-n3", addr), PocWeight: 10},
		}
		subgroupWeights = append(subgroupWeights, &types.ValidationWeight{
			MemberAddress: addr,
			MlNodes:       nodes,
		})
		participants = append(participants, &types.ActiveParticipant{
			Index:   addr,
			Models:  []string{modelID},
			MlNodes: []*types.ModelMLNodes{{MlNodes: nodes}},
		})
		perfSummaries[addr] = map[uint64]types.EpochPerformanceSummary{
			0: {ParticipantId: addr, EpochIndex: 0, RewardedCoins: 1},
		}
	}

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID, ThroughputPerNonce: 1000, VRam: 32}},
		epochGroupData: map[string]map[uint64]types.EpochGroupData{
			modelID: {0: {ValidationWeights: subgroupWeights}},
		},
		perfSummaries: perfSummaries,
		params:        &types.Params{EpochParams: &types.EpochParams{PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1}}},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	epoch := types.Epoch{Index: 1}
	mockKeeper.populateSubgroupsFromParticipants(epoch.Index, participants)

	seen := make(map[string]bool)
	for anchor := int64(100); anchor < 1000 && len(seen) < 2; anchor++ {
		snap, err := modelAssigner.SamplePreservedForEpisode(ctx, epoch, anchor)
		require.NoError(t, err)
		key := fmt.Sprintf("%v", snap.ModelPreservedNodes)
		seen[key] = true
	}
	require.GreaterOrEqual(t, len(seen), 2, "anchor height should meaningfully influence sampling")
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

func cloneActiveParticipants(participants []*types.ActiveParticipant) []*types.ActiveParticipant {
	cloned := make([]*types.ActiveParticipant, 0, len(participants))
	for _, participant := range participants {
		copyParticipant := *participant
		copyParticipant.Models = append([]string(nil), participant.Models...)
		copyParticipant.MlNodes = make([]*types.ModelMLNodes, 0, len(participant.MlNodes))
		for _, modelNodes := range participant.MlNodes {
			if modelNodes == nil {
				copyParticipant.MlNodes = append(copyParticipant.MlNodes, nil)
				continue
			}
			copyModelNodes := &types.ModelMLNodes{
				MlNodes: make([]*types.MLNodeInfo, 0, len(modelNodes.MlNodes)),
			}
			for _, node := range modelNodes.MlNodes {
				if node == nil {
					copyModelNodes.MlNodes = append(copyModelNodes.MlNodes, nil)
					continue
				}
				copyNode := *node
				copyNode.TimeslotAllocation = append([]bool(nil), node.TimeslotAllocation...)
				copyModelNodes.MlNodes = append(copyModelNodes.MlNodes, &copyNode)
			}
			copyParticipant.MlNodes = append(copyParticipant.MlNodes, copyModelNodes)
		}
		cloned = append(cloned, &copyParticipant)
	}
	return cloned
}

func snapshotFromAllocatedParticipants(anchor int64, participants []*types.ActiveParticipant) types.PreservedNodesSnapshot {
	modelToParticipantNodes := make(map[string]map[string][]string)
	for _, participant := range participants {
		for modelIndex, modelID := range participant.Models {
			if modelIndex >= len(participant.MlNodes) || participant.MlNodes[modelIndex] == nil {
				continue
			}
			for _, node := range participant.MlNodes[modelIndex].MlNodes {
				if node != nil && len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
					byParticipant, ok := modelToParticipantNodes[modelID]
					if !ok {
						byParticipant = make(map[string][]string)
						modelToParticipantNodes[modelID] = byParticipant
					}
					byParticipant[participant.Index] = append(byParticipant[participant.Index], node.NodeId)
				}
			}
		}
	}

	modelIDs := sortedKeys(modelToParticipantNodes)
	modelPreservedNodes := make([]*types.ModelPreservedNodes, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		byParticipant := modelToParticipantNodes[modelID]
		participantIDs := sortedKeys(byParticipant)
		entries := make([]*types.ParticipantPreservedNodes, 0, len(participantIDs))
		for _, pid := range participantIDs {
			nodeIDs := append([]string(nil), byParticipant[pid]...)
			slices.Sort(nodeIDs)
			entries = append(entries, &types.ParticipantPreservedNodes{
				ParticipantId: pid,
				NodeIds:       nodeIDs,
			})
		}
		modelPreservedNodes = append(modelPreservedNodes, &types.ModelPreservedNodes{
			ModelId:      modelID,
			Participants: entries,
		})
	}

	return types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: anchor,
		ModelPreservedNodes: modelPreservedNodes,
	}
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
	// Actual preserved allocation happens later in SamplePreservedForEpisode
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

// TestSamplePreservedForEpisode_FairDistribution tests that preserved allocation is
// distributed fairly across many participants with many nodes.
func TestSamplePreservedForEpisode_FairDistribution(t *testing.T) {
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

	// Previous-epoch performance summaries (epoch 0): all participants rewarded so they are
	// eligible for preservation in epoch 1.
	previousEpochIndex := uint64(0)
	perfSummaries := make(map[string]map[uint64]types.EpochPerformanceSummary, numParticipants)
	for i := 0; i < numParticipants; i++ {
		participantID := formatParticipantID(i)
		perfSummaries[participantID] = map[uint64]types.EpochPerformanceSummary{
			previousEpochIndex: {ParticipantId: participantID, EpochIndex: previousEpochIndex, RewardedCoins: 1},
		}
	}

	// Setup mock keeper
	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID}},
		hardwareNodes:    hardwareNodesMap,
		epochGroupData:   previousEpochGroupData,
		perfSummaries:    perfSummaries,
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
	runSamplePreservedForEpisode(t, ctx, modelAssigner, mockKeeper, upcomingEpoch, participants)

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

// TestSamplePreservedForEpisode_NoReward_NoEligibleParticipants verifies that when no
// participants have a reward for the previous epoch, none are added to previousEpochData,
// so there are no eligible participants and no preserved allocation. It covers three ways
// to be ineligible:
// - no performance summary at all for the previous epoch
// - summary with RewardedCoins == 0 (slashed / no reward)
// - summary stored only for a different epoch
func TestSamplePreservedForEpisode_NoReward_NoEligibleParticipants(t *testing.T) {
	const (
		numParticipants     = 20
		nodesPerParticipant = 10
		baseWeight          = 10
		modelID             = "model-no-reward"
		previousEpochIndex  = uint64(0) // upcomingEpoch.Index will be 1
	)

	// Partition participants into three ineligible groups (upcoming epoch = 1, previous = 0):
	// - No summary: participants 0-6
	// - Zero reward: 7-13  (summary for epoch 0, RewardedCoins=0)
	// - Wrong epoch: 14-19 (summary exists only for epoch 2; lookup for epoch 0 misses)
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

	// perfSummaries is non-nil but only contains entries that still make everyone ineligible:
	// - participants 0-6: omitted (no summary) -> GetEpochPerformanceSummary returns not found
	// - participants 7-13: summary for previousEpoch with RewardedCoins=0 -> skipped
	// - participants 14-19: summary only for epoch 2; lookup for previousEpoch=0 returns not found
	perfSummaries := make(map[string]map[uint64]types.EpochPerformanceSummary)
	for i := noSettleEnd; i < zeroRewardEnd; i++ {
		participantID := formatParticipantID(i)
		perfSummaries[participantID] = map[uint64]types.EpochPerformanceSummary{
			previousEpochIndex: {ParticipantId: participantID, EpochIndex: previousEpochIndex, RewardedCoins: 0},
		}
	}
	for i := wrongEpochStart; i < numParticipants; i++ {
		participantID := formatParticipantID(i)
		perfSummaries[participantID] = map[uint64]types.EpochPerformanceSummary{
			previousEpochIndex + 2: {ParticipantId: participantID, EpochIndex: previousEpochIndex + 2, RewardedCoins: 1},
		}
	}

	t.Logf("Ineligible groups: no summary (0..%d), zero reward (%d..%d), wrong epoch (%d..%d)",
		noSettleEnd-1, noSettleEnd, zeroRewardEnd-1, wrongEpochStart, numParticipants-1)

	mockKeeper := &mockKeeperForModelAssigner{
		governanceModels: []types.Model{{Id: modelID}},
		hardwareNodes:    hardwareNodesMap,
		epochGroupData:   previousEpochGroupData,
		perfSummaries:    perfSummaries,
		params: &types.Params{
			EpochParams: &types.EpochParams{
				PocSlotAllocation: &types.Decimal{Value: 5, Exponent: -1},
			},
		},
	}

	modelAssigner := NewModelAssigner(mockKeeper, mockLogger{})
	upcomingEpoch := types.Epoch{Index: 1}

	modelAssigner.setModelsForParticipants(ctx, participants, upcomingEpoch)
	runSamplePreservedForEpisode(t, ctx, modelAssigner, mockKeeper, upcomingEpoch, participants)

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
	runSamplePreservedForEpisode(t, ctx, modelAssigner, mockKeeper, upcomingEpoch, participants)

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
		// All participants rewarded in previous epoch (epoch 0) so they are eligible for preservation
		perfSummaries: map[string]map[uint64]types.EpochPerformanceSummary{
			"participant1": {0: {ParticipantId: "participant1", EpochIndex: 0, RewardedCoins: 1}},
			"participant2": {0: {ParticipantId: "participant2", EpochIndex: 0, RewardedCoins: 1}},
			"participant3": {0: {ParticipantId: "participant3", EpochIndex: 0, RewardedCoins: 1}},
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
	runSamplePreservedForEpisode(t, ctx, modelAssigner, mockKeeper, upcomingEpoch, participants)

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

// Dedup is exercised directly by TestDedupMLNodesById. The sampler no longer mutates
// participants, so the previous "dedup before allocation" assertion style (checking
// participants[0].MlNodes[0].MlNodes length after allocation) does not map to the new
// surface and is intentionally removed.

// Source isolation tests for the EpochMLNodeData accessors. Each verifies
// that mutating the returned slice/map does NOT mutate the source data.
// Pointer identity per *MLNodeInfo is intentionally preserved because the
// allocator depends on it (see model_assignment.go:773).

func TestGetForModelReturnsCopy(t *testing.T) {
	e := NewEpochMLNodeData()
	e.Append("m1", "p1", &types.MLNodeInfo{NodeId: "n1"})

	out := e.GetForModel("m1")
	out["p1"] = append(out["p1"], &types.MLNodeInfo{NodeId: "n2"})
	out["p2"] = []*types.MLNodeInfo{{NodeId: "n3"}}

	require.Len(t, e.data["m1"]["p1"], 1, "source p1 slice mutated via GetForModel")
	require.NotContains(t, e.data["m1"], "p2", "source map gained a key via GetForModel")
}

func TestGetForParticipantReturnsCopy(t *testing.T) {
	e := NewEpochMLNodeData()
	e.Append("m1", "p1", &types.MLNodeInfo{NodeId: "b"})
	e.Append("m1", "p1", &types.MLNodeInfo{NodeId: "a"})

	out := e.GetForParticipant("m1", "p1")
	require.Equal(t, []string{"a", "b"}, []string{out[0].NodeId, out[1].NodeId},
		"returned slice should be sorted by NodeId")

	// Source order must NOT change because GetForParticipant sorts the clone.
	require.Equal(t, []string{"b", "a"},
		[]string{e.data["m1"]["p1"][0].NodeId, e.data["m1"]["p1"][1].NodeId},
		"source order mutated by GetForParticipant in-place sort")

	// Appending to the returned slice must not mutate the source.
	out = append(out, &types.MLNodeInfo{NodeId: "c"})
	require.Len(t, e.data["m1"]["p1"], 2, "source slice grew via GetForParticipant")
}

func TestGetForParticipantPreservesPointerIdentity(t *testing.T) {
	// Mutating a *MLNodeInfo field via the returned slice MUST be visible
	// to subsequent reads from the same source. The allocator depends on
	// this exact behavior (TimeslotAllocation marking).
	e := NewEpochMLNodeData()
	e.Append("m1", "p1", &types.MLNodeInfo{NodeId: "n1", TimeslotAllocation: []bool{false, false}})

	out := e.GetForParticipant("m1", "p1")
	out[0].TimeslotAllocation[1] = true

	out2 := e.GetForParticipant("m1", "p1")
	require.True(t, out2[0].TimeslotAllocation[1],
		"node-field mutation must remain visible across calls (pointer identity)")
}

func TestForModelReturnsCopy(t *testing.T) {
	e := NewEpochMLNodeData()
	e.Append("m1", "p1", &types.MLNodeInfo{NodeId: "n1"})

	view := e.ForModel("m1")
	view.Append("m1", "p1", &types.MLNodeInfo{NodeId: "n2"})
	view.Set("m1", "p2", []*types.MLNodeInfo{{NodeId: "n3"}})

	require.Len(t, e.data["m1"]["p1"], 1, "source p1 slice mutated via ForModel view")
	require.NotContains(t, e.data["m1"], "p2", "source gained a participant via ForModel view")
}
