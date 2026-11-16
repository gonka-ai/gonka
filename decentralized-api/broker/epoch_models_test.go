package broker

import (
	"errors"
	"strings"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freshNodeState() *NodeState {
	return &NodeState{
		EpochModels:  map[string]types.Model{},
		EpochMLNodes: map[string]types.MLNodeInfo{},
	}
}

func twoModelPoCParams(epochModelsOnly bool) *pocParams {
	return &pocParams{
		models: map[string]apiconfig.PoCModelConfigCache{
			"model-a": {ModelId: "model-a", SeqLen: 128},
			"model-b": {ModelId: "model-b", SeqLen: 256},
		},
		epochModelsOnly: epochModelsOnly,
	}
}

func TestResolvePoCModelForNode_EpochModelsOnlyPicksOwnEpochModel(t *testing.T) {
	broker := NewTestBroker()
	broker.ownEpochModels = map[string]bool{"model-b": true}
	nodeModels := map[string]ModelArgs{"model-a": {}, "model-b": {}}

	model, ok := broker.resolvePoCModelForNode(freshNodeState(), nodeModels, twoModelPoCParams(true))
	require.True(t, ok)
	assert.Equal(t, "model-b", model.ModelId)

	model, ok = broker.resolvePoCModelForNode(freshNodeState(), nodeModels, twoModelPoCParams(false))
	require.True(t, ok)
	assert.Equal(t, "model-a", model.ModelId)
}

func TestResolvePoCModelForNode_EpochModelsOnlySkipsNodeWithoutOwnEpochModel(t *testing.T) {
	broker := NewTestBroker()
	broker.ownEpochModels = map[string]bool{"model-a": true}

	_, ok := broker.resolvePoCModelForNode(freshNodeState(), map[string]ModelArgs{"model-b": {}}, twoModelPoCParams(true))
	assert.False(t, ok)

	nodeState := freshNodeState()
	nodeState.EpochMLNodes["model-b"] = types.MLNodeInfo{NodeId: "node-1"}
	_, ok = broker.resolvePoCModelForNode(nodeState, map[string]ModelArgs{"model-b": {}}, twoModelPoCParams(true))
	assert.False(t, ok)
}

func TestResolvePoCModelForNode_UnknownEpochModelsDeferFreshNode(t *testing.T) {
	broker := NewTestBroker()
	nodeModels := map[string]ModelArgs{"model-a": {}, "model-b": {}}

	_, ok := broker.resolvePoCModelForNode(freshNodeState(), nodeModels, twoModelPoCParams(true))
	assert.False(t, ok)

	model, ok := broker.resolvePoCModelForNode(freshNodeState(), nodeModels, twoModelPoCParams(false))
	require.True(t, ok)
	assert.Equal(t, "model-a", model.ModelId)

	assigned := freshNodeState()
	assigned.EpochMLNodes["model-b"] = types.MLNodeInfo{NodeId: "node-1"}
	model, ok = broker.resolvePoCModelForNode(assigned, nodeModels, twoModelPoCParams(true))
	require.True(t, ok)
	assert.Equal(t, "model-b", model.ModelId)
}

func TestUpdateNodeWithEpochData_RecordsOwnEpochModels(t *testing.T) {
	mockChainBridge := &MockBrokerChainBridge{}
	broker := newTestBrokerWithChainBridge(mockChainBridge)
	participantAddress := broker.participantInfo.GetAddress()
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:     100,
			SubGroupModels: []string{"model-a", "model-b"},
			TotalWeight:    10,
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-a").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:        100,
			ModelSnapshot:     &types.Model{Id: "model-a"},
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: participantAddress}},
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-b").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:        100,
			ModelSnapshot:     &types.Model{Id: "model-b"},
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: "someone-else"}},
		},
	}, nil)

	require.NoError(t, broker.UpdateNodeWithEpochData(broker.phaseTracker.GetCurrentEpochState()))
	assert.Equal(t, map[string]bool{"model-a": true}, broker.ownEpochModels)
}

func TestUpdateNodeWithEpochData_IncompleteSubgroupsLeaveEpochModelsUnknownAndRetry(t *testing.T) {
	mockChainBridge := &MockBrokerChainBridge{}
	broker := newTestBrokerWithChainBridge(mockChainBridge)
	participantAddress := broker.participantInfo.GetAddress()
	broker.ownEpochModels = map[string]bool{"stale": true}
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:     100,
			SubGroupModels: []string{"model-a"},
			TotalWeight:    10,
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-a").Return(nil, errors.New("unavailable")).Once()
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-a").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:        100,
			ModelSnapshot:     &types.Model{Id: "model-a"},
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: participantAddress}},
		},
	}, nil).Once()
	epochState := broker.phaseTracker.GetCurrentEpochState()

	require.NoError(t, broker.UpdateNodeWithEpochData(epochState))
	assert.Nil(t, broker.ownEpochModels)
	assert.Zero(t, broker.lastEpochIndex)

	require.NoError(t, broker.UpdateNodeWithEpochData(epochState))
	assert.Equal(t, map[string]bool{"model-a": true}, broker.ownEpochModels)
	assert.Equal(t, uint64(100), broker.lastEpochIndex)
	mockChainBridge.AssertExpectations(t)
}

func TestUpdateNodeWithEpochData_MissingModelSnapshotLeavesEpochModelsUnknown(t *testing.T) {
	mockChainBridge := &MockBrokerChainBridge{}
	broker := newTestBrokerWithChainBridge(mockChainBridge)
	participantAddress := broker.participantInfo.GetAddress()
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:     100,
			SubGroupModels: []string{"model-a"},
			TotalWeight:    10,
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-a").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:        100,
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: participantAddress}},
		},
	}, nil)

	require.NoError(t, broker.UpdateNodeWithEpochData(broker.phaseTracker.GetCurrentEpochState()))
	assert.Nil(t, broker.ownEpochModels)
	assert.Zero(t, broker.lastEpochIndex)
}

func TestUpdateNodeWithEpochData_MatchesMemberAddressCaseInsensitively(t *testing.T) {
	mockChainBridge := &MockBrokerChainBridge{}
	broker := newTestBrokerWithChainBridge(mockChainBridge)
	participantAddress := broker.participantInfo.GetAddress()
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:     100,
			SubGroupModels: []string{"model-a"},
			TotalWeight:    10,
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-a").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:        100,
			ModelSnapshot:     &types.Model{Id: "model-a"},
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: strings.ToUpper(participantAddress)}},
		},
	}, nil)

	require.NoError(t, broker.UpdateNodeWithEpochData(broker.phaseTracker.GetCurrentEpochState()))
	assert.Equal(t, map[string]bool{"model-a": true}, broker.ownEpochModels)
}

func TestUpdateNodeWithEpochData_ConfirmationGenerationRecoversOwnModelAfterSubgroupFailure(t *testing.T) {
	mockChainBridge := &MockBrokerChainBridge{}
	broker := newTestBrokerWithChainBridge(mockChainBridge)
	participantAddress := broker.participantInfo.GetAddress()
	mockChainBridge.On("GetGovernanceModels").Return(&types.QueryModelsAllResponse{
		Model: []types.Model{{Id: "model-a"}, {Id: "model-b"}},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:     100,
			SubGroupModels: []string{"model-a", "model-b"},
			TotalWeight:    10,
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-a").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:        100,
			ModelSnapshot:     &types.Model{Id: "model-a"},
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: "someone-else"}},
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-b").Return(nil, errors.New("unavailable")).Once()
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model-b").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:    100,
			ModelSnapshot: &types.Model{Id: "model-b"},
			ValidationWeights: []*types.ValidationWeight{{
				MemberAddress: participantAddress,
				MlNodes:       []*types.MLNodeInfo{{NodeId: "node-1"}},
			}},
		},
	}, nil).Once()

	nodeModels := map[string]ModelArgs{"model-a": {}, "model-b": {}}
	broker.mu.Lock()
	broker.nodes["node-1"] = &NodeWithState{
		Node: Node{Id: "node-1", Models: nodeModels},
		State: NodeState{
			IntendedStatus:    types.HardwareNodeStatus_POC,
			PocIntendedStatus: PocStatusGenerating,
			EpochModels:       map[string]types.Model{},
			EpochMLNodes:      map[string]types.MLNodeInfo{},
		},
	}
	broker.mu.Unlock()
	params := twoModelPoCParams(true)
	params.startPoCBlockHeight = 500
	scheduledModel := func() string {
		broker.mu.RLock()
		state := broker.nodes["node-1"].State
		broker.mu.RUnlock()
		cmd, ok := broker.getCommandForState("node-1", &state, nodeModels, params, nil, 1, &types.ConfirmationPoCEvent{TriggerHeight: 500}).(StartPoCNodeCommandV2)
		if !ok {
			return ""
		}
		return cmd.Model
	}
	epochState := broker.phaseTracker.GetCurrentEpochState()

	require.NoError(t, broker.UpdateNodeWithEpochData(epochState))
	assert.Nil(t, broker.ownEpochModels)
	assert.Empty(t, scheduledModel())

	require.NoError(t, broker.UpdateNodeWithEpochData(epochState))
	assert.Equal(t, map[string]bool{"model-b": true}, broker.ownEpochModels)
	assert.Equal(t, "model-b", scheduledModel())
	mockChainBridge.AssertExpectations(t)
}
