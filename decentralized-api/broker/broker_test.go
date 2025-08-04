package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"

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

	return NewBroker(mockChainBridge, phaseTracker, participantInfo, "", mlnodeclient.NewMockClientFactory())
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
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
	runningNode := <-availableNode
	if runningNode == nil {
		t.Fatalf("expected node1, got nil")
	}
	if runningNode.Id != node.Id {
		t.Fatalf("expected node1, got: " + runningNode.Id)
	}
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got " + runningNode.Id)
	}
}

func registerNodeAndSetInferenceStatus(t *testing.T, broker *Broker, node apiconfig.InferenceNodeConfig) {
	nodeIsRegistered := make(chan *apiconfig.InferenceNodeConfig, 2)
	queueMessage(t, broker, RegisterNode{node, nodeIsRegistered})

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
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
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
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
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
	queueMessage(t, broker, LockAvailableNode{"model2", "", false, availableNode})
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
		queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
		if <-availableNode == nil {
			t.Fatalf("expected node1, got nil")
		}
	}
}

func TestVersionFiltering(t *testing.T) {
	broker := NewTestBroker()
	v1node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "v1node",
		MaxConcurrent: 1000,
		Version:       "v1",
	}
	novNode := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "novNode",
		MaxConcurrent: 1000,
		Version:       "",
	}
	registerNodeAndSetInferenceStatus(t, broker, v1node)
	registerNodeAndSetInferenceStatus(t, broker, novNode)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{"model1", "v1", false, availableNode})
	node := <-availableNode
	require.NotNil(t, node)
	require.Equal(t, "v1node", node.Id)
	queueMessage(t, broker, LockAvailableNode{"model1", "v1", false, availableNode})
	node = <-availableNode
	require.NotNil(t, node)
	require.Equal(t, "v1node", node.Id)
	queueMessage(t, broker, LockAvailableNode{"model1", "v2", false, availableNode})
	require.Nil(t, <-availableNode)
	queueMessage(t, broker, LockAvailableNode{"model1", "v2", true, availableNode})
	node = <-availableNode
	require.NotNil(t, node)
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
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node2",
		MaxConcurrent: 1,
	}
	registerNodeAndSetInferenceStatus(t, broker, node1)
	registerNodeAndSetInferenceStatus(t, broker, node2)

	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
	firstNode := <-availableNode
	if firstNode == nil {
		t.Fatalf("expected node1 or node2, got nil")
	}
	println("First Node: " + firstNode.Id)
	if firstNode.Id != node1.Id && firstNode.Id != node2.Id {
		t.Fatalf("expected node1 or node2, got: " + firstNode.Id)
	}
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
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
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
	runningNode := <-availableNode
	if runningNode == nil {
		t.Fatalf("expected node1, got nil")
	}
	if runningNode.Id != node.Id {
		t.Fatalf("expected node1, got: " + runningNode.Id)
	}
	release := make(chan bool, 2)
	queueMessage(t, broker, ReleaseNode{node.Id, InferenceSuccess{}, release})
	if !<-release {
		t.Fatalf("expected true, got false")
	}
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
	if <-availableNode == nil {
		t.Fatalf("expected node1, got nil")
	}

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
	queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
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
	if err := broker.QueueMessage(RegisterNode{node, make(chan *apiconfig.InferenceNodeConfig, 0)}); err == nil {
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
