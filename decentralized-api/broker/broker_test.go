package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
	"fmt"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slog"
)

type MockBrokerChainBridge struct {
	mock.Mock
}

func (m *MockBrokerChainBridge) GetHardwareNodes() (*types.QueryHardwareNodesResponse, error) {
	args := m.Called()
	return args.Get(0).(*types.QueryHardwareNodesResponse), args.Error(1)
}

func (m *MockBrokerChainBridge) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	args := m.Called(diff)
	return args.Error(0)
}

func (m *MockBrokerChainBridge) GetBlockHash(height int64) (string, error) {
	args := m.Called(height)
	return args.String(0), args.Error(1)
}

func (m *MockBrokerChainBridge) GetGovernanceModels() (*types.QueryModelsAllResponse, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryModelsAllResponse), args.Error(1)
}

func (m *MockBrokerChainBridge) GetCurrentEpochGroupData() (*types.QueryCurrentEpochGroupDataResponse, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryCurrentEpochGroupDataResponse), args.Error(1)
}

func (m *MockBrokerChainBridge) GetEpochGroupDataByModelId(pocHeight uint64, modelId string) (*types.QueryGetEpochGroupDataResponse, error) {
	args := m.Called(pocHeight, modelId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryGetEpochGroupDataResponse), args.Error(1)
}

func NewTestBroker() *Broker {
	participantInfo := participant.CosmosInfo{
		Address: "cosmos1dummyaddress",
		PubKey:  "dummyPubKey",
	}
	phaseTracker := chainphase.NewChainPhaseTracker()
	phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "hash-1"},
		&types.Epoch{Index: 0, PocStartBlockHeight: 0},
		&types.EpochParams{},
		true,
		nil,
	)

	mockChainBridge := &MockBrokerChainBridge{}
	mockChainBridge.On("GetGovernanceModels").Return(&types.QueryModelsAllResponse{
		Model: []types.Model{
			{Id: "model1"},
		},
	}, nil)

	// Setup meaningful mock responses for epoch data
	parentEpochData := &types.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			PocStartBlockHeight: 100,
			SubGroupModels:      []string{"model1"},
		},
	}
	model1EpochData := &types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			PocStartBlockHeight: 100,
			ModelSnapshot:       &types.Model{Id: "model1"},
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "cosmos1dummyaddress",
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "test-node-1"},
					},
				},
			},
		},
	}

	mockChainBridge.On("GetCurrentEpochGroupData").Return(parentEpochData, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", uint64(100), "model1").Return(model1EpochData, nil)

	mockConfigManager := &apiconfig.ConfigManager{}
	return NewBroker(mockChainBridge, phaseTracker, participantInfo, "", mlnodeclient.NewMockClientFactory(), mockConfigManager)
}

func TestSingleNode(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}

	registerNodeAndSetInferenceStatus(t, broker, node)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	runningNode := <-availableNode
	if runningNode == nil {
		t.Fatalf("expected node1, got nil")
	}
	if runningNode.Id != node.Id {
		t.Fatalf("expected node1, got: " + runningNode.Id)
	}
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got " + runningNode.Id)
	}
}

func registerNodeAndSetInferenceStatus(t *testing.T, broker *Broker, node apiconfig.InferenceNodeConfig) {
	cmd := NewRegisterNodeCommand(node)
	nodeIsRegistered := cmd.Response
	queueMessage(t, broker, cmd)

	// Wait for the 1st command to be propagated,
	// so our set status timestamp comes after the initial registration timestamp
	_ = <-nodeIsRegistered

	mlNode := types.MLNodeInfo{
		NodeId:             node.Id,
		Throughput:         0,
		PocWeight:          10,
		TimeslotAllocation: []bool{true, false},
	}

	var modelId string
	for m, _ := range node.Models {
		modelId = m
		break
	}
	if modelId == "" {
		t.Fatalf("expected modelId, got empty string")
	}
	model := types.Model{
		Id: modelId,
	}
	broker.UpdateNodeEpochData([]*types.MLNodeInfo{&mlNode}, modelId, model)

	inferenceUpCommand := NewInferenceUpAllCommand()
	queueMessage(t, broker, inferenceUpCommand)

	// Wait for InferenceUpAllCommand to complete
	<-inferenceUpCommand.Response

	setStatusCommand := NewSetNodesActualStatusCommand(
		[]StatusUpdate{
			{
				NodeId:     node.Id,
				PrevStatus: types.HardwareNodeStatus_UNKNOWN,
				NewStatus:  types.HardwareNodeStatus_INFERENCE,
				Timestamp:  time.Now(),
			},
		},
	)
	queueMessage(t, broker, setStatusCommand)

	<-setStatusCommand.Response

	time.Sleep(50 * time.Millisecond)
}

func TestNodeRemoval(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}

	registerNodeAndSetInferenceStatus(t, broker, node)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	runningNode := <-availableNode
	if runningNode == nil {
		t.Fatalf("expected node1, got nil")
	}
	if runningNode.Id != node.Id {
		t.Fatalf("expected node1, got: " + runningNode.Id)
	}
	release := make(chan bool, 2)
	queueMessage(t, broker, RemoveNode{node.Id, release})
	if !<-release {
		t.Fatalf("expected true, got false")
	}
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got node")
	}
}

func TestModelMismatch(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}

	registerNodeAndSetInferenceStatus(t, broker, node)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{Model: "model2", Response: availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got node1")
	}
}

func TestHighConcurrency(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 100,
	}

	registerNodeAndSetInferenceStatus(t, broker, node)

	availableNode := make(chan *Node, 2)
	for i := 0; i < 100; i++ {
		queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
		if <-availableNode == nil {
			t.Fatalf("expected node1, got nil")
		}
	}
}

func TestMultipleNodes(t *testing.T) {
	broker := NewTestBroker()
	node1 := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	node2 := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8081,
		PoCPort:       5001,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node2",
		MaxConcurrent: 1,
	}
	registerNodeAndSetInferenceStatus(t, broker, node1)
	registerNodeAndSetInferenceStatus(t, broker, node2)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	firstNode := <-availableNode
	if firstNode == nil {
		t.Fatalf("expected node1 or node2, got nil")
	}
	println("First Node: " + firstNode.Id)
	if firstNode.Id != node1.Id && firstNode.Id != node2.Id {
		t.Fatalf("expected node1 or node2, got: " + firstNode.Id)
	}
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	secondNode := <-availableNode
	if secondNode == nil {
		t.Fatalf("expected another node, got nil")
	}
	println("Second Node: " + secondNode.Id)
	if secondNode.Id == firstNode.Id {
		t.Fatalf("expected different node from 1, got: " + secondNode.Id)
	}
}

func queueMessage(t *testing.T, broker *Broker, command Command) {
	err := broker.QueueMessage(command)
	if err != nil {
		t.Fatalf("error sending message" + err.Error())
	}
}

func TestReleaseNode(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	registerNodeAndSetInferenceStatus(t, broker, node)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	runningNode := <-availableNode
	require.NotNil(t, runningNode)
	require.Equal(t, node.Id, runningNode.Id)
	release := make(chan bool, 2)
	queueMessage(t, broker, ReleaseNode{node.Id, InferenceSuccess{}, release})

	b := <-release
	require.True(t, b, "expected release response to be true")
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	require.NotNil(t, <-availableNode, "expected node1, got nil")
}

func TestRoundTripSegment(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:             "localhost",
		InferenceSegment: "/is",
		InferencePort:    8080,
		PoCSegment:       "/is",
		PoCPort:          5000,
		Models:           map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:               "node1",
		MaxConcurrent:    1,
	}
	registerNodeAndSetInferenceStatus(t, broker, node)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{Model: "model1", Response: availableNode})
	runningNode := <-availableNode
	if runningNode == nil {
		t.Fatalf("expected node1, got nil")
	}
	if runningNode.Id != node.Id {
		t.Fatalf("expected node1, got: " + runningNode.Id)
	}
	if runningNode.InferenceSegment != node.InferenceSegment {
		slog.Warn("Inference segment not matching", "expected", node, "got", runningNode)
		t.Fatalf("expected inference segment /is, got: " + runningNode.InferenceSegment)
	}
}

func TestCapacityCheck(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	if err := broker.QueueMessage(RegisterNode{node, make(chan NodeCommandResponse, 0)}); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestNodeShouldBeOperationalTest(t *testing.T) {
	adminState := AdminState{
		Enabled: true,
		Epoch:   10,
	}
	require.False(t, ShouldBeOperational(adminState, 10, types.PoCGeneratePhase))
	require.False(t, ShouldBeOperational(adminState, 10, types.PoCGenerateWindDownPhase))
	require.False(t, ShouldBeOperational(adminState, 10, types.PoCValidatePhase))
	require.False(t, ShouldBeOperational(adminState, 10, types.PoCValidateWindDownPhase))
	require.True(t, ShouldBeOperational(adminState, 10, types.InferencePhase))

	adminState = AdminState{
		Enabled: false,
		Epoch:   11,
	}
	require.True(t, ShouldBeOperational(adminState, 11, types.PoCGeneratePhase))
	require.True(t, ShouldBeOperational(adminState, 11, types.PoCGenerateWindDownPhase))
	require.True(t, ShouldBeOperational(adminState, 11, types.PoCValidatePhase))
	require.True(t, ShouldBeOperational(adminState, 11, types.PoCValidateWindDownPhase))
	require.True(t, ShouldBeOperational(adminState, 11, types.InferencePhase))

	require.False(t, ShouldBeOperational(adminState, 12, types.PoCGeneratePhase))
	require.False(t, ShouldBeOperational(adminState, 12, types.PoCGenerateWindDownPhase))
	require.False(t, ShouldBeOperational(adminState, 12, types.PoCValidatePhase))
	require.False(t, ShouldBeOperational(adminState, 12, types.PoCValidateWindDownPhase))
	require.False(t, ShouldBeOperational(adminState, 12, types.InferencePhase))
}

func TestVersionedUrls(t *testing.T) {
	node := Node{
		Host:             "example.com",
		InferencePort:    8080,
		InferenceSegment: "/api/v1",
		PoCPort:          9090,
		PoCSegment:       "/api/v1",
	}

	// Test InferenceUrl without version (backward compatibility)
	expectedInferenceUrl := "http://example.com:8080/api/v1"
	actualInferenceUrl := node.InferenceUrl()
	assert.Equal(t, expectedInferenceUrl, actualInferenceUrl)

	// Test InferenceUrlWithVersion with empty version (should fall back to non-versioned)
	actualInferenceUrlEmpty := node.InferenceUrlWithVersion("")
	assert.Equal(t, expectedInferenceUrl, actualInferenceUrlEmpty)

	// Test InferenceUrlWithVersion with version
	expectedVersionedInferenceUrl := "http://example.com:8080/v3.0.8/api/v1"
	actualVersionedInferenceUrl := node.InferenceUrlWithVersion("v3.0.8")
	assert.Equal(t, expectedVersionedInferenceUrl, actualVersionedInferenceUrl)

	// Test PoCUrl without version (backward compatibility)
	expectedPocUrl := "http://example.com:9090/api/v1"
	actualPocUrl := node.PoCUrl()
	assert.Equal(t, expectedPocUrl, actualPocUrl)

	// Test PoCUrlWithVersion with empty version (should fall back to non-versioned)
	actualPocUrlEmpty := node.PoCUrlWithVersion("")
	assert.Equal(t, expectedPocUrl, actualPocUrlEmpty)

	// Test PoCUrlWithVersion with version
	expectedVersionedPocUrl := "http://example.com:9090/v3.0.8/api/v1"
	actualVersionedPocUrl := node.PoCUrlWithVersion("v3.0.8")
	assert.Equal(t, expectedVersionedPocUrl, actualVersionedPocUrl)
}

func TestImmediateClientRefreshLogic(t *testing.T) {
	// Test the immediate client refresh logic
	broker := NewTestBroker()

	// Test case 1: Should not refresh when lastUsedVersion is empty (first time)
	assert.False(t, broker.configManager.ShouldRefreshClients(), "Should not refresh on first time")

	// Test the RefreshAllClients functionality by registering a node
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}

	registerNodeAndSetInferenceStatus(t, broker, node)

	// Get the worker and mock client factory
	worker, exists := broker.nodeWorkGroup.GetWorker("node1")
	require.True(t, exists, "Worker should exist")

	mockFactory := broker.mlNodeClientFactory.(*mlnodeclient.MockClientFactory)

	// Get the client using the actual key that would be used
	allClients := mockFactory.GetAllClients()
	var mockClient *mlnodeclient.MockClient
	for _, client := range allClients {
		mockClient = client
		break // Get the first (and likely only) client
	}
	require.NotNil(t, mockClient, "Mock client should exist")

	initialStopCalled := mockClient.StopCalled

	// Dynamic client creation means refresh is effectively a no-op for the HTTP client.
	worker.RefreshClientImmediate("v3.0.8", "v3.1.0")
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, initialStopCalled, mockClient.StopCalled, "Stop should not be invoked when clients are created per request")

	worker.RefreshClientImmediate("v3.1.0", "v3.2.0")
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, initialStopCalled, mockClient.StopCalled, "Stop should remain unchanged on repeated refreshes")
}

func TestUpdateNodeConfiguration(t *testing.T) {
	broker := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}

	registerNodeAndSetInferenceStatus(t, broker, node)

	// Capture initial node info
	nodesBefore, err := broker.GetNodes()
	require.NoError(t, err)
	require.Equal(t, 1, len(nodesBefore))
	before := nodesBefore[0]
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, before.State.CurrentStatus)
	beforeNodeNum := before.Node.NodeNum

	// Get mock client and capture StopCalled baseline
	mockFactory := broker.mlNodeClientFactory.(*mlnodeclient.MockClientFactory)
	var mockClient *mlnodeclient.MockClient
	for _, c := range mockFactory.GetAllClients() {
		mockClient = c
		break
	}
	require.NotNil(t, mockClient, "Mock client should exist")

	// Prepare an update: change host, ports, models, maxConcurrent, hardware
	updated := apiconfig.InferenceNodeConfig{
		Host:             "127.0.0.1",
		InferenceSegment: "/api",
		InferencePort:    9090,
		PoCSegment:       "/api",
		PoCPort:          5050,
		Models:           map[string]apiconfig.ModelConfig{"model1": {Args: []string{"--foo", "bar"}}},
		Id:               "node1",
		MaxConcurrent:    3,
		Hardware:         []apiconfig.Hardware{{Type: "GPU", Count: 2}},
	}

	// Queue UpdateNode
	command := NewUpdateNodeCommand(updated)
	resp := command.Response
	err = broker.QueueMessage(command)
	require.NoError(t, err)
	out := <-resp
	require.NotNil(t, out)
	require.NotNil(t, out.Node)
	require.Nil(t, out.Error)

	// Validate updated view
	nodesAfter, err := broker.GetNodes()
	require.NoError(t, err)
	require.Equal(t, 1, len(nodesAfter))
	after := nodesAfter[0]

	assert.Equal(t, updated.Host, after.Node.Host)
	assert.Equal(t, updated.InferenceSegment, after.Node.InferenceSegment)
	assert.Equal(t, updated.InferencePort, after.Node.InferencePort)
	assert.Equal(t, updated.PoCSegment, after.Node.PoCSegment)
	assert.Equal(t, updated.PoCPort, after.Node.PoCPort)
	assert.Equal(t, updated.MaxConcurrent, after.Node.MaxConcurrent)
	assert.Equal(t, beforeNodeNum, after.Node.NodeNum, "NodeNum should be preserved")
	assert.Equal(t, types.HardwareNodeStatus_INFERENCE, after.State.CurrentStatus, "Current status should remain unchanged")

	// Validate models args updated
	require.Contains(t, after.Node.Models, "model1")
	assert.Equal(t, []string{"--foo", "bar"}, after.Node.Models["model1"].Args)
}

func TestValidateInferenceNode_FieldCorrectness(t *testing.T) {
	broker := NewTestBroker()

	tests := []struct {
		name    string
		node    apiconfig.InferenceNodeConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid node",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: false,
		},
		{
			name: "empty node id",
			node: apiconfig.InferenceNodeConfig{
				Id:               "",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "node id is required",
		},
		{
			name: "whitespace only node id",
			node: apiconfig.InferenceNodeConfig{
				Id:               "   ",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "node id is required",
		},
		{
			name: "empty host",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "host is required",
		},
		{
			name: "inference port too low",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    0,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "inference_port must be between 1 and 65535",
		},
		{
			name: "inference port too high",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    65536,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "inference_port must be between 1 and 65535",
		},
		{
			name: "poc port too low",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          0,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "poc_port must be between 1 and 65535",
		},
		{
			name: "poc port too high",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          70000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "poc_port must be between 1 and 65535",
		},
		{
			name: "max concurrent zero",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    0,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "max_concurrent must be greater than 0",
		},
		{
			name: "max concurrent negative",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    -1,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "max_concurrent must be greater than 0",
		},
		{
			name: "no models",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{},
			},
			wantErr: true,
			errMsg:  "at least one model must be specified",
		},
		{
			name: "nil models",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           nil,
			},
			wantErr: true,
			errMsg:  "at least one model must be specified",
		},
		{
			name: "empty segments are allowed",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    8080,
				PoCPort:          5000,
				InferenceSegment: "",
				PoCSegment:       "",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: false,
		},
		{
			name: "valid port boundaries",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node1",
				Host:             "localhost",
				InferencePort:    1,
				PoCPort:          65535,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    1,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := broker.validateInferenceNode(tt.node, "")
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateInferenceNode_StandardConfigs(t *testing.T) {
	broker := NewTestBroker()

	nodes := []apiconfig.InferenceNodeConfig{
		{
			Id:            "node1",
			Host:          "inference",
			InferencePort: 5000,
			PoCPort:       8080,
			MaxConcurrent: 500,
			Models: map[string]apiconfig.ModelConfig{
				"Qwen/Qwen3-32B-FP8": {
					Args: []string{},
				},
			},
		},
		{
			Id:            "node1",
			Host:          "inference",
			InferencePort: 5000,
			PoCPort:       5000,
			MaxConcurrent: 500,
			Models: map[string]apiconfig.ModelConfig{
				"Qwen/Qwen3-32B-FP8": {
					Args: []string{},
				},
			},
		},
		{
			Id:            "node1",
			Host:          "inference",
			InferencePort: 5000,
			PoCPort:       8080,
			MaxConcurrent: 500,
			Models: map[string]apiconfig.ModelConfig{
				"Qwen/Qwen2.5-7B-Instruct": {
					Args: []string{
						"--quantization", "fp8",
						"--gpu-memory-utilization", "0.9",
					},
				},
			},
		},
		{
			Id:            "node1",
			Host:          "inference",
			InferencePort: 5000,
			PoCPort:       8080,
			MaxConcurrent: 500,
			Models: map[string]apiconfig.ModelConfig{
				"Qwen/Qwen2.5-7B-Instruct": {
					Args: []string{
						"--quantization", "fp8",
						"--tensor-parallel-size", "4",
						"--pipeline-parallel-size", "2",
					},
				},
			},
		},
		{
			Id:            "node1",
			Host:          "inference",
			InferencePort: 5000,
			PoCPort:       8080,
			MaxConcurrent: 500,
			Models: map[string]apiconfig.ModelConfig{
				"Qwen/QwQ-32B": {
					Args: []string{
						"--quantization", "fp8",
						"--kv-cache-dtype", "fp8",
					},
				},
			},
		},
		{
			Id:            "node1",
			Host:          "inference",
			InferencePort: 5000,
			PoCPort:       8080,
			MaxConcurrent: 500,
			Models: map[string]apiconfig.ModelConfig{
				"Qwen/QwQ-32B": {
					Args: []string{
						"--quantization", "fp8",
						"--tensor-parallel-size", "4",
						"--kv-cache-dtype", "fp8",
					},
				},
			},
		},
		{
			Id:            "node1",
			Host:          "inference",
			InferencePort: 5000,
			PoCPort:       8080,
			MaxConcurrent: 500,
			Models: map[string]apiconfig.ModelConfig{
				"Qwen/QwQ-32B": {
					Args: []string{
						"--quantization", "fp8",
						"--tensor-parallel-size", "4",
						"--pipeline-parallel-size", "2",
						"--kv-cache-dtype", "fp8",
					},
				},
			},
		},
	}

	for i, node := range nodes {
		t.Run(fmt.Sprintf("QwenConfig%d", i+1), func(t *testing.T) {
			require.NoError(t, broker.validateInferenceNode(node, ""))
		})
	}
}

func TestValidateInferenceNode_HostPortUniqueness(t *testing.T) {
	broker := NewTestBroker()

	// Register first node
	node1 := apiconfig.InferenceNodeConfig{
		Id:               "node1",
		Host:             "localhost",
		InferencePort:    8080,
		PoCPort:          5000,
		InferenceSegment: "/api",
		PoCSegment:       "/api",
		MaxConcurrent:    5,
		Models:           map[string]apiconfig.ModelConfig{"model1": {}},
	}

	cmd := NewRegisterNodeCommand(node1)
	err := broker.QueueMessage(cmd)
	require.NoError(t, err)
	response := <-cmd.Response
	require.NotNil(t, response)
	require.Nil(t, response.Error)
	require.NotNil(t, response.Node)

	// Give broker time to process the registration
	time.Sleep(50 * time.Millisecond)

	tests := []struct {
		name    string
		node    apiconfig.InferenceNodeConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "duplicate inference host+port",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node2",
				Host:             "localhost",
				InferencePort:    8080, // Same as node1
				PoCPort:          6000, // Different PoC port
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "duplicate inference host+port combination",
		},
		{
			name: "duplicate poc host+port",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node3",
				Host:             "localhost",
				InferencePort:    8081, // Different inference port
				PoCPort:          5000, // Same as node1
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "duplicate PoC host+port combination",
		},
		{
			name: "different host, same ports - should be valid",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node4",
				Host:             "127.0.0.1", // Different host
				InferencePort:    8080,        // Same ports
				PoCPort:          5000,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: false,
		},
		{
			name: "same host, different ports - should be valid",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node5",
				Host:             "localhost",
				InferencePort:    8081, // Different ports
				PoCPort:          5001,
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: false,
		},
		{
			name: "both ports duplicate on same host",
			node: apiconfig.InferenceNodeConfig{
				Id:               "node6",
				Host:             "localhost",
				InferencePort:    8080, // Same as node1
				PoCPort:          5000, // Same as node1
				InferenceSegment: "/api",
				PoCSegment:       "/api",
				MaxConcurrent:    5,
				Models:           map[string]apiconfig.ModelConfig{"model1": {}},
			},
			wantErr: true,
			errMsg:  "duplicate inference host+port combination", // Should catch inference port first
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := broker.validateInferenceNode(tt.node, "")
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateInferenceNode_UpdateExcludesSelf(t *testing.T) {
	broker := NewTestBroker()

	// Register first node
	node1 := apiconfig.InferenceNodeConfig{
		Id:               "node1",
		Host:             "localhost",
		InferencePort:    8080,
		PoCPort:          5000,
		InferenceSegment: "/api",
		PoCSegment:       "/api",
		MaxConcurrent:    5,
		Models:           map[string]apiconfig.ModelConfig{"model1": {}},
	}

	cmd := NewRegisterNodeCommand(node1)
	err := broker.QueueMessage(cmd)
	require.NoError(t, err)
	response1 := <-cmd.Response
	require.NotNil(t, response1)
	require.Nil(t, response1.Error)
	require.NotNil(t, response1.Node)

	// Give broker time to process the registration
	time.Sleep(50 * time.Millisecond)

	// Register second node with different ports
	node2 := apiconfig.InferenceNodeConfig{
		Id:               "node2",
		Host:             "localhost",
		InferencePort:    8081,
		PoCPort:          5001,
		InferenceSegment: "/api",
		PoCSegment:       "/api",
		MaxConcurrent:    5,
		Models:           map[string]apiconfig.ModelConfig{"model1": {}},
	}

	cmd2 := NewRegisterNodeCommand(node2)
	err = broker.QueueMessage(cmd2)
	require.NoError(t, err)
	response2 := <-cmd2.Response
	require.NotNil(t, response2)
	require.Nil(t, response2.Error)
	require.NotNil(t, response2.Node)

	// Give broker time to process the second registration
	time.Sleep(50 * time.Millisecond)

	// Update node1 to use node2's ports - should fail (duplicate)
	updatedNode1 := apiconfig.InferenceNodeConfig{
		Id:               "node1",
		Host:             "localhost",
		InferencePort:    8081, // node2's port
		PoCPort:          5001, // node2's port
		InferenceSegment: "/api",
		PoCSegment:       "/api",
		MaxConcurrent:    5,
		Models:           map[string]apiconfig.ModelConfig{"model1": {}},
	}

	err = broker.validateInferenceNode(updatedNode1, "node1") // Exclude node1 from check
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")

	// Update node1 to use same ports as itself - should succeed (excluded from check)
	samePortsNode1 := apiconfig.InferenceNodeConfig{
		Id:               "node1",
		Host:             "localhost",
		InferencePort:    8080, // node1's original port
		PoCPort:          5000, // node1's original port
		InferenceSegment: "/api",
		PoCSegment:       "/api",
		MaxConcurrent:    10, // Changed max concurrent
		Models:           map[string]apiconfig.ModelConfig{"model1": {}},
	}

	err = broker.validateInferenceNode(samePortsNode1, "node1") // Exclude node1 from check
	require.NoError(t, err, "Should allow updating node to keep same ports when excluding self")
}

// registerTwoNodesOnSameHost is a helper function that registers 2 nodes with the same host but different ports
// and asserts both are successfully registered. Returns the broker and both node configs.
// If node1 or node2 are nil, default configurations will be used.
func registerTwoNodesOnSameHost(t *testing.T, broker *Broker, node1 *apiconfig.InferenceNodeConfig, node2 *apiconfig.InferenceNodeConfig) (apiconfig.InferenceNodeConfig, apiconfig.InferenceNodeConfig) {
	defaultNode1 := apiconfig.InferenceNodeConfig{
		Id:            "node1",
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		MaxConcurrent: 5,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
	}

	defaultNode2 := apiconfig.InferenceNodeConfig{
		Id:            "node2",
		Host:          "localhost",
		InferencePort: 8081,
		PoCPort:       5001,
		MaxConcurrent: 5,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
	}

	// Use provided nodes or defaults
	if node1 == nil {
		node1 = &defaultNode1
	}
	if node2 == nil {
		node2 = &defaultNode2
	}

	node1Config := *node1
	node2Config := *node2

	// Register node1
	cmd1 := NewRegisterNodeCommand(node1Config)
	err := broker.QueueMessage(cmd1)
	require.NoError(t, err)
	response1 := <-cmd1.Response
	require.NotNil(t, response1)
	require.Nil(t, response1.Error, "node1 registration should succeed")
	require.NotNil(t, response1.Node)
	require.Equal(t, node1Config.Id, response1.Node.Id)

	// Give broker time to process the registration
	time.Sleep(50 * time.Millisecond)

	// Register node2
	cmd2 := NewRegisterNodeCommand(node2Config)
	err = broker.QueueMessage(cmd2)
	require.NoError(t, err)
	response2 := <-cmd2.Response
	require.NotNil(t, response2)
	require.Nil(t, response2.Error, "node2 registration should succeed")
	require.NotNil(t, response2.Node)
	require.Equal(t, node2Config.Id, response2.Node.Id)

	// Give broker time to process the second registration
	time.Sleep(50 * time.Millisecond)

	// Verify both nodes are registered
	nodes, err := broker.GetNodes()
	require.NoError(t, err)
	require.Equal(t, 2, len(nodes), "Both nodes should be registered")

	// Verify node IDs
	nodeIds := make(map[string]bool)
	for _, node := range nodes {
		nodeIds[node.Node.Id] = true
	}
	require.True(t, nodeIds[node1Config.Id], "node1 should be registered")
	require.True(t, nodeIds[node2Config.Id], "node2 should be registered")

	return node1Config, node2Config
}

func TestRegisterTwoNodesOnSameHost(t *testing.T) {
	broker := NewTestBroker()
	node1, node2 := registerTwoNodesOnSameHost(t, broker, nil, nil)

	// Additional verification: check ports are different
	nodes, err := broker.GetNodes()
	require.NoError(t, err)

	var foundNode1, foundNode2 *NodeResponse
	for i := range nodes {
		if nodes[i].Node.Id == node1.Id {
			foundNode1 = &nodes[i]
		}
		if nodes[i].Node.Id == node2.Id {
			foundNode2 = &nodes[i]
		}
	}

	require.NotNil(t, foundNode1, "node1 should be found")
	require.NotNil(t, foundNode2, "node2 should be found")
	require.Equal(t, node1.InferencePort, foundNode1.Node.InferencePort)
	require.Equal(t, node1.PoCPort, foundNode1.Node.PoCPort)
	require.Equal(t, node2.InferencePort, foundNode2.Node.InferencePort)
	require.Equal(t, node2.PoCPort, foundNode2.Node.PoCPort)
	require.NotEqual(t, foundNode1.Node.InferencePort, foundNode2.Node.InferencePort, "Inference ports should be different")
	require.NotEqual(t, foundNode1.Node.PoCPort, foundNode2.Node.PoCPort, "PoC ports should be different")
}

func TestUpdateNodePortCollision(t *testing.T) {
	broker := NewTestBroker()
	node1, node2 := registerTwoNodesOnSameHost(t, broker, nil, nil)

	// Try to update node2 to use node1's ports - should fail
	updatedNode2 := apiconfig.InferenceNodeConfig{
		Id:            node2.Id,
		Host:          node2.Host,
		InferencePort: node1.InferencePort, // Collision with node1
		PoCPort:       node1.PoCPort,       // Collision with node1
		MaxConcurrent: node2.MaxConcurrent,
		Models:        node2.Models,
	}

	cmd := NewUpdateNodeCommand(updatedNode2)
	err := broker.QueueMessage(cmd)
	require.NoError(t, err)
	response := <-cmd.Response
	require.NotNil(t, response)
	require.NotNil(t, response.Error, "Update should fail due to port collision")
	require.Contains(t, response.Error.Error(), "duplicate", "Error should mention duplicate ports")
	require.Nil(t, response.Node, "Node should be nil on error")

	// Verify node2's ports haven't changed
	nodes, err := broker.GetNodes()
	require.NoError(t, err)
	var foundNode2 *NodeResponse
	for i := range nodes {
		if nodes[i].Node.Id == node2.Id {
			foundNode2 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode2)
	require.Equal(t, node2.InferencePort, foundNode2.Node.InferencePort, "node2 inference port should remain unchanged")
	require.Equal(t, node2.PoCPort, foundNode2.Node.PoCPort, "node2 PoC port should remain unchanged")
}

func TestUpdateNodeNoCollision(t *testing.T) {
	broker := NewTestBroker()
	node1, node2 := registerTwoNodesOnSameHost(t, broker, nil, nil)

	// Update node2 to use different ports that don't collide with node1
	updatedNode2 := apiconfig.InferenceNodeConfig{
		Id:            node2.Id,
		Host:          node2.Host,
		InferencePort: 8082, // Different from both node1 (8080) and original node2 (8081)
		PoCPort:       5002, // Different from both node1 (5000) and original node2 (5001)
		MaxConcurrent: node2.MaxConcurrent,
		Models:        node2.Models,
	}

	cmd := NewUpdateNodeCommand(updatedNode2)
	err := broker.QueueMessage(cmd)
	require.NoError(t, err)
	response := <-cmd.Response
	require.NotNil(t, response)
	require.Nil(t, response.Error, "Update should succeed")
	require.NotNil(t, response.Node)
	require.Equal(t, node2.Id, response.Node.Id)

	// Give broker time to process the update
	time.Sleep(50 * time.Millisecond)

	// Verify node2's ports have been updated
	nodes, err := broker.GetNodes()
	require.NoError(t, err)
	var foundNode2 *NodeResponse
	for i := range nodes {
		if nodes[i].Node.Id == node2.Id {
			foundNode2 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode2)
	require.Equal(t, updatedNode2.InferencePort, foundNode2.Node.InferencePort, "node2 inference port should be updated")
	require.Equal(t, updatedNode2.PoCPort, foundNode2.Node.PoCPort, "node2 PoC port should be updated")

	// Verify node1's ports remain unchanged
	var foundNode1 *NodeResponse
	for i := range nodes {
		if nodes[i].Node.Id == node1.Id {
			foundNode1 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode1)
	require.Equal(t, node1.InferencePort, foundNode1.Node.InferencePort, "node1 inference port should remain unchanged")
	require.Equal(t, node1.PoCPort, foundNode1.Node.PoCPort, "node1 PoC port should remain unchanged")
}

func TestUpdateNodeSwapPorts(t *testing.T) {
	broker := NewTestBroker()
	node1, node2 := registerTwoNodesOnSameHost(t, broker, nil, nil)

	// Swap PoC and inference ports of node2
	// Original: InferencePort=8081, PoCPort=5001
	// After swap: InferencePort=5001, PoCPort=8081
	swappedNode2 := apiconfig.InferenceNodeConfig{
		Id:            node2.Id,
		Host:          node2.Host,
		InferencePort: node2.PoCPort,       // Swap: use old PoC port for inference
		PoCPort:       node2.InferencePort, // Swap: use old inference port for PoC
		MaxConcurrent: node2.MaxConcurrent,
		Models:        node2.Models,
	}

	cmd := NewUpdateNodeCommand(swappedNode2)
	err := broker.QueueMessage(cmd)
	require.NoError(t, err)
	response := <-cmd.Response
	require.NotNil(t, response)
	require.Nil(t, response.Error, "Update should succeed when swapping ports")
	require.NotNil(t, response.Node)
	require.Equal(t, node2.Id, response.Node.Id)

	// Give broker time to process the update
	time.Sleep(50 * time.Millisecond)

	// Verify node2's ports have been swapped
	nodes, err := broker.GetNodes()
	require.NoError(t, err)
	var foundNode2 *NodeResponse
	for i := range nodes {
		if nodes[i].Node.Id == node2.Id {
			foundNode2 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode2)
	require.Equal(t, swappedNode2.InferencePort, foundNode2.Node.InferencePort, "node2 inference port should be swapped")
	require.Equal(t, swappedNode2.PoCPort, foundNode2.Node.PoCPort, "node2 PoC port should be swapped")
	require.Equal(t, node2.PoCPort, foundNode2.Node.InferencePort, "Inference port should equal original PoC port")
	require.Equal(t, node2.InferencePort, foundNode2.Node.PoCPort, "PoC port should equal original inference port")

	// Verify no collision occurred - node1 should still have its original ports
	var foundNode1 *NodeResponse
	for i := range nodes {
		if nodes[i].Node.Id == node1.Id {
			foundNode1 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode1)
	require.Equal(t, node1.InferencePort, foundNode1.Node.InferencePort, "node1 inference port should remain unchanged")
	require.Equal(t, node1.PoCPort, foundNode1.Node.PoCPort, "node1 PoC port should remain unchanged")
}

func TestUpdateNodeHostCollisionWithPortChange(t *testing.T) {
	broker := NewTestBroker()

	// Setup: Two nodes with same ports but different hosts
	node1 := &apiconfig.InferenceNodeConfig{
		Id:            "node1",
		Host:          "host1",
		InferencePort: 8080,
		PoCPort:       5000,
		MaxConcurrent: 5,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
	}

	node2 := &apiconfig.InferenceNodeConfig{
		Id:            "node2",
		Host:          "host2",
		InferencePort: 8080, // Same ports as node1
		PoCPort:       5000, // Same ports as node1
		MaxConcurrent: 5,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
	}

	// Register both nodes
	registeredNode1, registeredNode2 := registerTwoNodesOnSameHost(t, broker, node1, node2)

	// Verify initial state: same ports, different hosts
	nodes, err := broker.GetNodes()
	require.NoError(t, err)
	var foundNode1, foundNode2 *NodeResponse
	for i := range nodes {
		if nodes[i].Node.Id == registeredNode1.Id {
			foundNode1 = &nodes[i]
		}
		if nodes[i].Node.Id == registeredNode2.Id {
			foundNode2 = &nodes[i]
		}
	}
	require.NotNil(t, foundNode1)
	require.NotNil(t, foundNode2)
	require.Equal(t, registeredNode1.InferencePort, foundNode2.Node.InferencePort, "Initial ports should be the same")
	require.Equal(t, registeredNode1.PoCPort, foundNode2.Node.PoCPort, "Initial PoC ports should be the same")
	require.NotEqual(t, foundNode1.Node.Host, foundNode2.Node.Host, "Initial hosts should be different")

	// Step 1: Try to update node2 to have the same host as node1 (causing collision) - should fail
	updatedNode2SameHost := apiconfig.InferenceNodeConfig{
		Id:            registeredNode2.Id,
		Host:          registeredNode1.Host,          // Same host as node1
		InferencePort: registeredNode2.InferencePort, // Same ports (collision!)
		PoCPort:       registeredNode2.PoCPort,       // Same ports (collision!)
		MaxConcurrent: registeredNode2.MaxConcurrent,
		Models:        registeredNode2.Models,
	}

	cmd1 := NewUpdateNodeCommand(updatedNode2SameHost)
	err = broker.QueueMessage(cmd1)
	require.NoError(t, err)
	response1 := <-cmd1.Response
	require.NotNil(t, response1)
	require.NotNil(t, response1.Error, "Update should fail due to port collision when host becomes the same")
	require.Contains(t, response1.Error.Error(), "duplicate", "Error should mention duplicate ports")
	require.Nil(t, response1.Node, "Node should be nil on error")

	// Verify node2 hasn't changed
	time.Sleep(50 * time.Millisecond)
	nodes, err = broker.GetNodes()
	require.NoError(t, err)
	for i := range nodes {
		if nodes[i].Node.Id == registeredNode2.Id {
			foundNode2 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode2)
	require.Equal(t, registeredNode2.Host, foundNode2.Node.Host, "node2 host should remain unchanged after failed update")
	require.Equal(t, registeredNode2.InferencePort, foundNode2.Node.InferencePort, "node2 inference port should remain unchanged")
	require.Equal(t, registeredNode2.PoCPort, foundNode2.Node.PoCPort, "node2 PoC port should remain unchanged")

	// Step 2: Update node2 to have the same host as node1 but change ports to avoid collision - should succeed
	updatedNode2DifferentPorts := apiconfig.InferenceNodeConfig{
		Id:            registeredNode2.Id,
		Host:          registeredNode1.Host, // Same host as node1
		InferencePort: 8082,                 // Different port to avoid collision
		PoCPort:       5002,                 // Different port to avoid collision
		MaxConcurrent: registeredNode2.MaxConcurrent,
		Models:        registeredNode2.Models,
	}

	cmd2 := NewUpdateNodeCommand(updatedNode2DifferentPorts)
	err = broker.QueueMessage(cmd2)
	require.NoError(t, err)
	response2 := <-cmd2.Response
	require.NotNil(t, response2)
	require.Nil(t, response2.Error, "Update should succeed when changing ports to avoid collision")
	require.NotNil(t, response2.Node)
	require.Equal(t, registeredNode2.Id, response2.Node.Id)

	// Give broker time to process the update
	time.Sleep(50 * time.Millisecond)

	// Verify node2 has been updated successfully
	nodes, err = broker.GetNodes()
	require.NoError(t, err)
	for i := range nodes {
		if nodes[i].Node.Id == registeredNode2.Id {
			foundNode2 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode2)
	require.Equal(t, updatedNode2DifferentPorts.Host, foundNode2.Node.Host, "node2 host should be updated to match node1")
	require.Equal(t, updatedNode2DifferentPorts.InferencePort, foundNode2.Node.InferencePort, "node2 inference port should be updated")
	require.Equal(t, updatedNode2DifferentPorts.PoCPort, foundNode2.Node.PoCPort, "node2 PoC port should be updated")

	// Verify node1 remains unchanged
	for i := range nodes {
		if nodes[i].Node.Id == registeredNode1.Id {
			foundNode1 = &nodes[i]
			break
		}
	}
	require.NotNil(t, foundNode1)
	require.Equal(t, registeredNode1.Host, foundNode1.Node.Host, "node1 host should remain unchanged")
	require.Equal(t, registeredNode1.InferencePort, foundNode1.Node.InferencePort, "node1 inference port should remain unchanged")
	require.Equal(t, registeredNode1.PoCPort, foundNode1.Node.PoCPort, "node1 PoC port should remain unchanged")

	// Verify both nodes now have the same host but different ports
	require.Equal(t, foundNode1.Node.Host, foundNode2.Node.Host, "Both nodes should have the same host")
	require.NotEqual(t, foundNode1.Node.InferencePort, foundNode2.Node.InferencePort, "Inference ports should be different")
	require.NotEqual(t, foundNode1.Node.PoCPort, foundNode2.Node.PoCPort, "PoC ports should be different")
}
