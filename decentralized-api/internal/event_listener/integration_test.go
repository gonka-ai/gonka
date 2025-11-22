package event_listener

import (
	"context"
	"decentralized-api/internal/poc"
	"decentralized-api/internal/validation"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/productscience/inference/testutil/keeper"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

var defaultEpochParams = types.EpochParams{
	EpochLength:           100,
	EpochShift:            0,
	EpochMultiplier:       1,
	PocStageDuration:      20,
	PocExchangeDuration:   2,
	PocValidationDelay:    2,
	PocValidationDuration: 10,
}

var defaultReconciliationConfig = MlNodeReconciliationConfig{
	Inference: &MlNodeStageReconciliationConfig{
		BlockInterval: 50,
		TimeInterval:  60 * time.Hour,
	},
	PoC: &MlNodeStageReconciliationConfig{
		BlockInterval: 1,
		TimeInterval:  60 * time.Hour,
	},
	LastTime: time.Now(),
}

// Mock implementations using minimal interfaces
type MockOrchestratorChainBridge struct {
}

func (m MockOrchestratorChainBridge) PoCBatchesForStage(startPoCBlockHeight int64) (*types.QueryPocBatchesForStageResponse, error) {
	return &types.QueryPocBatchesForStageResponse{
		PocBatch: []types.PoCBatchesWithParticipants{
			{
				Participant: "participant-1",
				PubKey:      "pubkey-1",
				HexPubKey:   "hex-pubkey-1",
				PocBatch: []types.PoCBatch{
					{
						ParticipantAddress:       "participant-1",
						PocStageStartBlockHeight: startPoCBlockHeight,
						ReceivedAtBlockHeight:    startPoCBlockHeight + 1,
						Nonces:                   []int64{1, 2, 3},
						Dist:                     []float64{0, 0, 0},
						BatchId:                  "batch-1",
					},
				},
			},
		},
	}, nil
}

func (m MockOrchestratorChainBridge) GetBlockHash(height int64) (string, error) {
	return fmt.Sprintf("block-hash-%d", height), nil
}

func (m MockOrchestratorChainBridge) GetPocParams() (*types.PocParams, error) {
	return &types.PocParams{
		ValidationSampleSize: 200,
	}, nil
}

type MockBrokerChainBridge struct {
	mock.Mock
}

func (m *MockBrokerChainBridge) GetParticipantAddress() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockBrokerChainBridge) GetHardwareNodes() (*types.QueryHardwareNodesResponse, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryHardwareNodesResponse), nil
}

func (m *MockBrokerChainBridge) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	args := m.Called(diff)
	return args.Error(0)
}

func (m *MockBrokerChainBridge) GetBlockHash(height int64) (string, error) {
	return "block-hash-" + strconv.FormatInt(height, 10), nil
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

type MockRandomSeedManager struct {
	mock.Mock
}

func (m *MockRandomSeedManager) ChangeCurrentSeed() {
	m.Called()
}

func (m *MockRandomSeedManager) GetSeedForEpoch(epochIndex uint64) apiconfig.SeedInfo {
	m.Called()
	return apiconfig.SeedInfo{}
}

func (m *MockRandomSeedManager) RequestMoney(epochIndex uint64) {
	m.Called()
}

func (m *MockRandomSeedManager) CreateNewSeed(epochIndex uint64) (*apiconfig.SeedInfo, error) {
	m.Called()
	return nil, nil
}

func (m *MockRandomSeedManager) GenerateSeedInfo(epochIndex uint64) {
	m.Called(epochIndex)
}

type MockQueryClient struct {
	mock.Mock
}

func (m *MockQueryClient) EpochInfo(ctx context.Context, req *types.QueryEpochInfoRequest, opts ...grpc.CallOption) (*types.QueryEpochInfoResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(*types.QueryEpochInfoResponse), args.Error(1)
}

func (m *MockQueryClient) Params(ctx context.Context, req *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryParamsResponse), args.Error(1)
}

// Test setup helpers

type IntegrationTestSetup struct {
	Dispatcher        *OnNewBlockDispatcher
	NodeBroker        *broker.Broker
	PoCOrchestrator   poc.NodePoCOrchestrator
	PhaseTracker      *chainphase.ChainPhaseTracker
	MockClientFactory *mlnodeclient.MockClientFactory
	MockChainBridge   *MockBrokerChainBridge
	MockQueryClient   *MockQueryClient
	MockSeedManager   *MockRandomSeedManager
	EpochParams       *types.EpochParams
}

func createIntegrationTestSetup(reconcilialtionConfig *MlNodeReconciliationConfig, params *types.EpochParams) *IntegrationTestSetup {
	mockQueryClient := &MockQueryClient{}
	mockSeedManager := &MockRandomSeedManager{}

	phaseTracker := chainphase.NewChainPhaseTracker()

	// Create mock client factory that tracks calls
	mockClientFactory := mlnodeclient.NewMockClientFactory()

	// Create real broker with mocked chain bridge
	mockChainBridge := &MockBrokerChainBridge{}
	participantInfo := participant.CosmosInfo{
		Address: "some-address",
		PubKey:  "some-pub-key",
	}
	mockConfigManager := &apiconfig.ConfigManager{}
	nodeBroker := broker.NewBroker(mockChainBridge, phaseTracker, &participantInfo, "http://localhost:8080/poc", mockClientFactory, mockConfigManager)

	// Create real PoC orchestrator (not mocked - we want to test the real flow)
	pocOrchestrator := poc.NewNodePoCOrchestrator(
		"some-pub-key",
		nodeBroker,
		"http://localhost:8080/poc",
		&MockOrchestratorChainBridge{},
		phaseTracker,
	)

	// Mock status function
	mockStatusFunc := func() (*coretypes.ResultStatus, error) {
		return &coretypes.ResultStatus{
			SyncInfo: coretypes.SyncInfo{CatchingUp: false},
		}, nil
	}

	mockSetHeightFunc := func(height int64) error {
		return nil
	}

	var paramsToReturn *types.EpochParams = &defaultEpochParams
	if params != nil {
		paramsToReturn = params
	}

	// Setup default mock behaviors
	mockChainBridge.On("GetHardwareNodes").Return(&types.QueryHardwareNodesResponse{Nodes: &types.HardwareNodes{HardwareNodes: []*types.HardwareNode{}}}, nil)
	mockChainBridge.On("GetParticipantAddress").Return("some-address")
	mockChainBridge.On("SubmitHardwareDiff", mock.Anything).Return(nil)
	mockChainBridge.On("GetGovernanceModels").Return(&types.QueryModelsAllResponse{
		Model: keeper.GenesisModelsTestList(),
	}, nil)
	mockChainBridge.On("GetCurrentEpochGroupData").Return(&types.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			PocStartBlockHeight: 100,
			SubGroupModels:      []string{"test-model"},
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", mock.AnythingOfType("uint64"), "").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			PocStartBlockHeight: 100,
			SubGroupModels:      []string{"test-model"},
		},
	}, nil)
	mockChainBridge.On("GetEpochGroupDataByModelId", mock.AnythingOfType("uint64"), "test-model").Return(&types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			ModelSnapshot: &types.Model{Id: "test-model"},
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: "some-address",
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "node-1"},
						{NodeId: "node-2"},
					},
				},
			},
		},
	}, nil)

	mockQueryClient.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{
		Params: types.Params{
			EpochParams: paramsToReturn,
		},
		// Empty epoch for now
		LatestEpoch: types.Epoch{},
	}, nil)

	// Setup mock for Params method
	validationParams := &types.ValidationParams{
		TimestampExpiration: 10,
		TimestampAdvance:    10,
	}
	mockQueryClient.On("Params", mock.MatchedBy(func(ctx context.Context) bool {
		return true // Match any context
	}), mock.AnythingOfType("*types.QueryParamsRequest")).Return(&types.QueryParamsResponse{
		Params: types.Params{
			ValidationParams: validationParams,
		},
	}, nil)

	// Setup mock expectations for RandomSeedManager
	mockSeedManager.On("ChangeCurrentSeed").Return()
	mockSeedManager.On("RequestMoney").Return()
	mockSeedManager.On("GenerateSeedInfo", mock.AnythingOfType("uint64")).Return()
	mockSeedManager.On("CreateNewSeed", mock.AnythingOfType("uint64")).Return()
	mockSeedManager.On("GetSeedForEpoch").Return(apiconfig.SeedInfo{})

	var finalReconciliationConfig MlNodeReconciliationConfig
	if reconcilialtionConfig == nil {
		finalReconciliationConfig = defaultReconciliationConfig
	} else {
		finalReconciliationConfig = *reconcilialtionConfig
	}
	// Create dispatcher with mocked dependencies
	mockValidator := &validation.InferenceValidator{}
	dispatcher := NewOnNewBlockDispatcher(
		nodeBroker,
		pocOrchestrator,
		mockQueryClient,
		phaseTracker,
		mockStatusFunc,
		mockSetHeightFunc,
		mockSeedManager,
		finalReconciliationConfig,
		mockConfigManager,
		mockValidator,
	)

	return &IntegrationTestSetup{
		Dispatcher:        dispatcher,
		NodeBroker:        nodeBroker,
		PoCOrchestrator:   pocOrchestrator,
		PhaseTracker:      phaseTracker,
		MockClientFactory: mockClientFactory,
		MockChainBridge:   mockChainBridge,
		MockQueryClient:   mockQueryClient,
		MockSeedManager:   mockSeedManager,
		EpochParams:       paramsToReturn,
	}
}

func (setup *IntegrationTestSetup) addTestNode(nodeId string, port int) {
	node := apiconfig.InferenceNodeConfig{
		Id:               nodeId,
		Host:             "localhost",
		InferenceSegment: "/inference",
		InferencePort:    port - 1, // Use different ports to distinguish nodes
		PoCSegment:       "/poc",
		PoCPort:          port,
		MaxConcurrent:    1,
		Models: map[string]apiconfig.ModelConfig{
			keeper.GenesisModelsTest_QWQ: {Args: []string{}},
		},
		Hardware: []apiconfig.Hardware{
			{Type: "GPU", Count: 1},
		},
	}

	responseChan := setup.NodeBroker.LoadNodeToBroker(&node)

	// Wait for the node to be loaded
	response := <-responseChan
	if response.Error != nil || response.Node == nil {
		panic(fmt.Sprintf("failed to register node %s: %v", nodeId, response.Error))
	}
}

func (setup *IntegrationTestSetup) advanceBlockHeight(blockHeight int64) {
	resp, err := setup.MockQueryClient.EpochInfo(context.Background(), &types.QueryEpochInfoRequest{})
	if err != nil {
		panic(err)
	}

	setup.setLatestEpoch(blockHeight, resp.LatestEpoch)
}

func (setup *IntegrationTestSetup) setLatestEpoch(blockHeight int64, epoch types.Epoch) {
	setup.MockQueryClient.ExpectedCalls = nil
	setup.MockQueryClient.On("EpochInfo", mock.Anything, mock.Anything).Return(&types.QueryEpochInfoResponse{
		BlockHeight: blockHeight,
		Params: types.Params{
			EpochParams: setup.EpochParams,
		},
		LatestEpoch: epoch,
	}, nil)
}

func (setup *IntegrationTestSetup) transitionChainStateToNextEpoch(blockHeight int64) {
	epochInfo, err := setup.MockQueryClient.EpochInfo(context.Background(), &types.QueryEpochInfoRequest{})
	if err != nil || epochInfo == nil {
		panic(fmt.Sprintf("Failed to get epoch info: %v", err))
	}

	newEpoch := types.Epoch{
		Index:               epochInfo.LatestEpoch.Index + 1,
		PocStartBlockHeight: blockHeight,
	}

	setup.setLatestEpoch(blockHeight, newEpoch)
}

func (setup *IntegrationTestSetup) setNodeAdminState(nodeId string, enabled bool) error {
	response := make(chan error, 1)
	err := setup.NodeBroker.QueueMessage(broker.SetNodeAdminStateCommand{
		NodeId:   nodeId,
		Enabled:  enabled,
		Response: response,
	})
	if err != nil {
		return err
	}
	return <-response
}

func (setup *IntegrationTestSetup) simulateBlock(height int64) error {
	// Now call to chain mock will return new blockHeight
	setup.advanceBlockHeight(height)

	blockInfo := chainphase.BlockInfo{
		Height: height,
		Hash:   fmt.Sprintf("hash-%d", height),
	}
	return setup.Dispatcher.ProcessNewBlock(context.Background(), blockInfo)
}

func (setup *IntegrationTestSetup) getNodeClient(nodeId string, port int) *mlnodeclient.MockClient {
	// Construct URLs the same way the broker does
	pocUrl := fmt.Sprintf("http://localhost:%d/poc", port)
	inferenceUrl := fmt.Sprintf("http://localhost:8080/inference")

	client := setup.MockClientFactory.GetClientForNode(pocUrl)
	if client == nil {
		// Create the client if it doesn't exist (should have been created by node registration)
		setup.MockClientFactory.CreateClient(pocUrl, inferenceUrl)
		client = setup.MockClientFactory.GetClientForNode(pocUrl)
		if client == nil {
			panic(fmt.Sprintf("Mock client is still nil after creation for pocUrl: %s", pocUrl))
		}
	}

	return client
}

func (setup *IntegrationTestSetup) assertNode(nodeId string, assertion func(n broker.NodeResponse)) {
	nodes, err := setup.NodeBroker.GetNodes()
	if err != nil {
		panic(err)
	}

	for _, node := range nodes {
		if node.Node.Id == nodeId {
			assertion(node)
			return
		}
	}
}

func (setup *IntegrationTestSetup) getNode(nodeId string) (*broker.Node, *broker.NodeState) {
	nodes, err := setup.NodeBroker.GetNodes()
	if err != nil {
		panic(err)
	}

	for _, node := range nodes {
		if node.Node.Id == nodeId {
			return &node.Node, &node.State
		}
	}

	panic("node not found")
}

func waitForAsync(duration time.Duration) {
	time.Sleep(duration)
}

func waitForNodeStatus(t *testing.T, setup *IntegrationTestSetup, nodeId string, expectedStatus types.HardwareNodeStatus, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, state := setup.getNode(nodeId)
		if state.CurrentStatus == expectedStatus {
			return // Success
		}
		time.Sleep(50 * time.Millisecond) // Poll interval
	}
	// If the loop finishes, the condition was not met in time.
	_, state := setup.getNode(nodeId)
	require.Equal(t, expectedStatus, state.CurrentStatus, "timed out waiting for node status")
}

func testreconcilialtionConfig(blockInterval int) MlNodeReconciliationConfig {
	return MlNodeReconciliationConfig{
		Inference: &MlNodeStageReconciliationConfig{
			BlockInterval: blockInterval,
			TimeInterval:  60 * time.Minute,
		},
		PoC: &MlNodeStageReconciliationConfig{
			BlockInterval: 1,
			TimeInterval:  60 * time.Minute,
		},
		LastTime:        time.Now(),
		LastBlockHeight: 0,
	}
}

func TestInferenceReconciliation(t *testing.T) {
	epochParams := defaultEpochParams
	reconciliationConfig := testreconcilialtionConfig(5)
	setup := createIntegrationTestSetup(&reconciliationConfig, &epochParams)

	setup.addTestNode("node-1", 8081)
	waitForNodeStatus(t, setup, "node-1", types.HardwareNodeStatus_STOPPED, 2*time.Second)

	setup.addTestNode("node-2", 8082)
	waitForNodeStatus(t, setup, "node-2", types.HardwareNodeStatus_STOPPED, 2*time.Second)

	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_STOPPED, n.State.CurrentStatus)
		require.Equal(t, types.HardwareNodeStatus_UNKNOWN, n.State.IntendedStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_STOPPED, n.State.CurrentStatus)
		require.Equal(t, types.HardwareNodeStatus_UNKNOWN, n.State.IntendedStatus)
	})

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)
	assertNodeClient(t, NodeClientAssertion{0, 0, 0, 0}, node1Client)
	assertNodeClient(t, NodeClientAssertion{0, 0, 0, 0}, node2Client)

	var i = int64(1)
	for i <= int64(reconciliationConfig.Inference.BlockInterval) {
		err := setup.simulateBlock(i)
		require.NoError(t, err)

		i++
	}

	waitForAsync(500 * time.Millisecond)

	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.IntendedStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.IntendedStatus)
	})

	expected := NodeClientAssertion{1, 0, 0, 1}
	assertNodeClient(t, expected, node1Client)
	assertNodeClient(t, expected, node2Client)

	for i < setup.EpochParams.EpochLength {
		i++
	}

	assertNodeClient(t, expected, node1Client)
	assertNodeClient(t, expected, node2Client)
}

func TestRegularPocScenario(t *testing.T) {
	epochParams := defaultEpochParams
	setup := createIntegrationTestSetup(nil, &epochParams)

	// Add two nodes - both initially enabled
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)
	assertNodeClient(t, NodeClientAssertion{0, 0, 0, 0}, node1Client)
	assertNodeClient(t, NodeClientAssertion{0, 0, 0, 0}, node2Client)

	var i int64 = 1
	for i <= setup.EpochParams.EpochLength {
		require.Equal(t, 0, node1Client.InitGenerateCalled, "InitGenerate was called. n = %d. i = %d", node1Client.InitGenerateCalled, i)
		require.Equal(t, 0, node2Client.InitGenerateCalled, "InitGenerate was called. n = %d. i = %d", node2Client.InitGenerateCalled, i)
		if i == setup.EpochParams.EpochLength {
			setup.transitionChainStateToNextEpoch(i)
		}
		err := setup.simulateBlock(i)
		require.NoError(t, err)

		i++
	}

	time.Sleep(100 * time.Millisecond)

	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.IntendedStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.IntendedStatus)
	})

	// +1 stop call for inference reconciliation
	expected := NodeClientAssertion{StopCalled: 2, InitGenerateCalled: 1, InitValidateCalled: 0, InferenceUpCalled: 1}
	assertNodeClient(t, expected, node1Client)
	assertNodeClient(t, expected, node2Client)

	pocGenEnd := setup.EpochParams.EpochLength + setup.EpochParams.GetEndOfPoCStage()
	for i < pocGenEnd {
		err := setup.simulateBlock(i)
		require.NoError(t, err)

		// Expect no new calls to ml node client
		expected := NodeClientAssertion{StopCalled: 2, InitGenerateCalled: 1, InitValidateCalled: 0, InferenceUpCalled: 1}
		assertNodeClient(t, expected, node1Client)
		assertNodeClient(t, expected, node2Client)
		i++
	}

	pocValStart := i
	pocValEnd := pocValStart + setup.EpochParams.PocValidationDelay + setup.EpochParams.PocValidationDuration
	for i < pocValEnd {
		err := setup.simulateBlock(i)
		require.NoError(t, err)

		if i == pocValStart {
			waitForAsync(300 * time.Millisecond)
		}

		expected := NodeClientAssertion{StopCalled: 2, InitGenerateCalled: 1, InitValidateCalled: 1, InferenceUpCalled: 1}
		assertNodeClient(t, expected, node1Client)
		assertNodeClient(t, expected, node2Client)

		i++
	}
	require.Equal(t, pocValEnd, i)

	require.Equal(t, node1Client.LastInitDto.BlockHeight, node1Client.LastInitValidateDto.BlockHeight)
	require.Equal(t, node1Client.LastInitDto.BlockHash, node1Client.LastInitValidateDto.BlockHash)
	require.Equal(t, node2Client.LastInitDto.BlockHeight, node2Client.LastInitValidateDto.BlockHeight)
	require.Equal(t, node2Client.LastInitDto.BlockHash, node2Client.LastInitValidateDto.BlockHash)

	require.Equal(t, node1Client.LastInitValidateDto.BlockHeight, node2Client.LastInitValidateDto.BlockHeight)
	require.Equal(t, node1Client.LastInitValidateDto.BlockHash, node2Client.LastInitValidateDto.BlockHash)

	err := setup.simulateBlock(i)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	expected = NodeClientAssertion{StopCalled: 3, InitGenerateCalled: 1, InitValidateCalled: 1, InferenceUpCalled: 2}
	assertNodeClient(t, expected, node1Client)
	assertNodeClient(t, expected, node2Client)
	setup.assertNode("node-1", func(n broker.NodeResponse) {
		assert.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.IntendedStatus)
		assert.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		assert.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.IntendedStatus)
		assert.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
	})
}

func TestNodeUpdateSwitchesPocAddresses(t *testing.T) {
	reconciliationConfig := testreconcilialtionConfig(4)
	epochParams := defaultEpochParams
	setup := createIntegrationTestSetup(&reconciliationConfig, &epochParams)

	const (
		nodeID      = "node-1"
		initialPort = 8091
	)

	setup.addTestNode(nodeID, initialPort)
	waitForNodeStatus(t, setup, nodeID, types.HardwareNodeStatus_STOPPED, 2*time.Second)

	nodeClient := setup.getNodeClient(nodeID, initialPort)

	var height int64 = 1
	for height <= int64(reconciliationConfig.Inference.BlockInterval) {
		require.NoError(t, setup.simulateBlock(height))
		height++
	}
	waitForAsync(200 * time.Millisecond)
	waitForNodeStatus(t, setup, nodeID, types.HardwareNodeStatus_INFERENCE, 2*time.Second)

	assertNodeClient(t, NodeClientAssertion{StopCalled: 1, InitGenerateCalled: 0, InitValidateCalled: 0, InferenceUpCalled: 1}, nodeClient)

	nodes, err := setup.NodeBroker.GetNodes()
	require.NoError(t, err)
	require.Equal(t, 1, len(nodes))
	originalNode := nodes[0].Node

	oldPocURL := fmt.Sprintf("http://%s:%d%s", originalNode.Host, originalNode.PoCPort, originalNode.PoCSegment)
	oldClient := setup.MockClientFactory.GetClientForNode(oldPocURL)
	require.NotNil(t, oldClient, "expected ML client for original address")

	updatedHost := "node1-updated-host"
	updatedInferencePort := 18081
	updatedPocPort := 18082
	updatedConfig := apiconfig.InferenceNodeConfig{
		Id:               nodeID,
		Host:             updatedHost,
		InferenceSegment: originalNode.InferenceSegment,
		InferencePort:    updatedInferencePort,
		PoCSegment:       originalNode.PoCSegment,
		PoCPort:          updatedPocPort,
		MaxConcurrent:    originalNode.MaxConcurrent,
		Models:           make(map[string]apiconfig.ModelConfig),
		Hardware:         originalNode.Hardware,
	}
	for modelID, args := range originalNode.Models {
		updatedConfig.Models[modelID] = apiconfig.ModelConfig{Args: args.Args}
	}

	updateCmd := broker.NewUpdateNodeCommand(updatedConfig)
	require.NoError(t, setup.NodeBroker.QueueMessage(updateCmd))
	updateResp := <-updateCmd.Response
	require.NotNil(t, updateResp)
	require.Nil(t, updateResp.Error)

	for height <= int64(setup.EpochParams.EpochLength) {
		if height == int64(setup.EpochParams.EpochLength) {
			setup.transitionChainStateToNextEpoch(height)
		}
		require.NoError(t, setup.simulateBlock(height))
		height++
	}

	waitForAsync(500 * time.Millisecond)

	newPocURL := fmt.Sprintf("http://%s:%d%s", updatedHost, updatedPocPort, originalNode.PoCSegment)
	newClient := setup.MockClientFactory.GetClientForNode(newPocURL)
	require.NotNil(t, newClient, "expected ML client to be recreated for updated address")

	newClient.WithTryLock(t, func() {
		assert.Greater(t, newClient.InitGenerateCalled, 0, "PoC should use updated address")
	})
	oldClient.WithTryLock(t, func() {
		assert.Equal(t, 0, oldClient.InitGenerateCalled, "PoC should not use stale address")
	})
}

type NodeClientAssertion struct {
	StopCalled         int
	InitGenerateCalled int
	InitValidateCalled int
	InferenceUpCalled  int
}

func assertNodeClient(t *testing.T, expected NodeClientAssertion, nodeClient *mlnodeclient.MockClient) {
	lock := nodeClient.Mu.TryLock()
	if !lock {
		t.Fatal("Failed to acquire lock on nodeClient")
	} else {
		defer nodeClient.Mu.Unlock()
	}

	require.Equal(t, expected.InitGenerateCalled, nodeClient.InitGenerateCalled, "InitGenerate was called. n = %d", nodeClient.InitGenerateCalled)
	require.Equal(t, expected.InitValidateCalled, nodeClient.InitValidateCalled, "InitValidate was called. n = %d", nodeClient.InitValidateCalled)
	require.Equal(t, expected.InferenceUpCalled, nodeClient.InferenceUpCalled, "InferenceUp was called. n = %d", nodeClient.InferenceUpCalled)
	require.Equal(t, expected.StopCalled, nodeClient.StopCalled, "Stop was called. n = %d", nodeClient.StopCalled)
}

// Test Scenario 1: Node disable scenario - node should skip PoC when disabled
func TestNodeDisableScenario_Integration(t *testing.T) {
	reconciliationConfig := testreconcilialtionConfig(5)
	epochParams := &types.EpochParams{
		EpochLength:           100,
		EpochShift:            0,
		EpochMultiplier:       1,
		PocStageDuration:      20,
		PocExchangeDuration:   2,
		PocValidationDelay:    2,
		PocValidationDuration: 10,
	}
	setup := createIntegrationTestSetup(&reconciliationConfig, epochParams)

	// Add two nodes - both initially enabled
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	// Disable node-1 before the PoC starts
	err := setup.setNodeAdminState("node-1", false)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, false, n.State.AdminState.Enabled)
		require.Equal(t, uint64(0), n.State.AdminState.Epoch)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, true, n.State.AdminState.Enabled)
		require.Equal(t, uint64(0), n.State.AdminState.Epoch)
	})

	// Simulate epoch PoC phase (block 100) to avoid same-epoch restrictions
	// Only node-2 should participate since node-1 is disabled
	latestEpoch := types.Epoch{
		Index:               1,
		PocStartBlockHeight: epochParams.EpochLength,
	}

	var i = setup.EpochParams.EpochLength
	setup.setLatestEpoch(i, latestEpoch)
	ec := types.NewEpochContext(latestEpoch, *setup.EpochParams)

	for i < 2*setup.EpochParams.EpochLength {
		err = setup.simulateBlock(i)
		require.NoError(t, err)

		// TODO: overall feels like a hack, should we just unconditionally wait after each block?
		//  or maybe add some explicit sync mechanism that would notify subscribers when all commands are processed?
		if ec.IsStartOfPocStage(i) ||
			ec.IsEndOfPoCStage(i) ||
			ec.IsStartOfPoCValidationStage(i) ||
			ec.IsEndOfPoCValidationStage(i) {
			println("Simulating block:", i, "ec.IsStartOfPocStage == ", ec.IsStartOfPocStage(i), "ec.IsEndOfPoCValidationStage == ", ec.IsEndOfPoCValidationStage(i))
			// Wait for all commands to finish so we don't cancel them too soon
			waitForAsync(500 * time.Millisecond)
		}

		i++
	}

	waitForAsync(300 * time.Millisecond)

	// Verify only node-2 received PoC start command, node-1 should be excluded
	node1Client.WithTryLock(t, func() {
		assert.Equal(t, 0, node1Client.InitGenerateCalled, "Disabled node-1 should NOT receive InitGenerate call")
		assert.Equal(t, 0, node1Client.InitValidateCalled, "Disabled node-1 should NOT receive InitGenerate call")
	})
	node2Client.WithTryLock(t, func() {
		assert.Equal(t, 1, node2Client.InitGenerateCalled, "Enabled node-2 should receive InitGenerate call")
		assert.Equal(t, 1, node2Client.InitValidateCalled, "Enabled node-2 should receive InitGenerate call")
	})

	node1Expected := NodeClientAssertion{StopCalled: 1, InitGenerateCalled: 0, InitValidateCalled: 0, InferenceUpCalled: 1}
	assertNodeClient(t, node1Expected, node1Client)
	setup.assertNode("node-1", func(n broker.NodeResponse) {
		// Default state is inference
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
	})

	node2Expected := NodeClientAssertion{StopCalled: 1, InitGenerateCalled: 1, InitValidateCalled: 1, InferenceUpCalled: 1}
	assertNodeClient(t, node2Expected, node2Client)
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
	})
}

// Test Scenario 2: Node enable scenario - node should participate in PoC after being enabled
func TestNodeEnableScenario_Integration(t *testing.T) {
	reconciliationConfig := testreconcilialtionConfig(4)
	setup := createIntegrationTestSetup(&reconciliationConfig, nil)

	// Add two nodes - node-1 initially disabled, node-2 enabled
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	// Disable node-1 initially
	err := setup.setNodeAdminState("node-1", false)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, false, n.State.AdminState.Enabled)
		require.Equal(t, uint64(0), n.State.AdminState.Epoch)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, true, n.State.AdminState.Enabled)
		require.Equal(t, uint64(0), n.State.AdminState.Epoch)
	})

	// Simulate first PoC (block 100) - only node-2 should participate
	setup.transitionChainStateToNextEpoch(100)
	err = setup.simulateBlock(100)
	require.NoError(t, err)

	// Give time for processing
	waitForAsync(500 * time.Millisecond)

	// Verify only node-2 received PoC start command
	require.Equal(t, 0, node1Client.InitGenerateCalled, "Disabled node-1 should NOT receive InitGenerate call")
	require.Equal(t, 1, node2Client.InitGenerateCalled, "Enabled node-2 should receive InitGenerate call")
	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
	})

	// Enable node-1 during inference phase
	err = setup.setNodeAdminState("node-1", true)
	require.NoError(t, err)
	waitForAsync(300 * time.Millisecond)

	var i = int64(150)
	for i < int64(150+reconciliationConfig.Inference.BlockInterval) {
		err = setup.simulateBlock(i)
		require.NoError(t, err)
		i++
	}
	waitForAsync(300 * time.Millisecond)

	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_INFERENCE, n.State.CurrentStatus)
	})

	// Simulate next epoch PoC (block 200) - both nodes should participate
	setup.transitionChainStateToNextEpoch(200)
	err = setup.simulateBlock(200)
	require.NoError(t, err)

	// Give time for processing
	waitForAsync(500 * time.Millisecond)

	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
	})

	// Verify both nodes received PoC start command
	require.Equal(t, 1, node1Client.InitGenerateCalled, "Node-1 should receive InitGenerate call after being enabled")
	require.Equal(t, 2, node2Client.InitGenerateCalled, "Node-2 should continue to receive InitGenerate call")
}

// Test Scenario 4: Full epoch transition with PoC commands
func TestFullEpochTransitionWithPocCommands_Integration(t *testing.T) {
	setup := createIntegrationTestSetup(nil, nil)

	// Add two nodes
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	assertNodeClient(t, NodeClientAssertion{0, 0, 0, 0}, node1Client)
	assertNodeClient(t, NodeClientAssertion{0, 0, 0, 0}, node2Client)

	// Simulate PoC start (block 0)
	setup.transitionChainStateToNextEpoch(100)
	err := setup.simulateBlock(100)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	// Both nodes should start PoC
	assert.Greater(t, node1Client.InitGenerateCalled, 0, "Node-1 should start PoC")
	assert.Greater(t, node2Client.InitGenerateCalled, 0, "Node-2 should start PoC")

	// Simulate end of PoC stage (block 20)
	err = setup.simulateBlock(120)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	assert.Equal(t, node1Client.InitValidateCalled, 1, "Node-1 should receive validation command")
	assert.Equal(t, node2Client.InitValidateCalled, 1, "Node-2 should receive validation command")

	// Simulate PoC validation start (block 22)
	err = setup.simulateBlock(122)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	// Nodes should receive validation commands

	// Simulate end of validation (block 32)
	err = setup.simulateBlock(132)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	// Nodes should receive inference up commands
	assert.Greater(t, node1Client.InferenceUpCalled, 0, "Node-1 should receive InferenceUp command")
	assert.Greater(t, node2Client.InferenceUpCalled, 0, "Node-2 should receive InferenceUp command")

	t.Logf("✅ Test 4 passed: Full epoch transition with proper PoC and validation commands")
}

func TestBasicSetup(t *testing.T) {
	reconcilialtionConfig := testreconcilialtionConfig(5)
	setup := createIntegrationTestSetup(&reconcilialtionConfig, nil)
	require.NotNil(t, setup)
	require.NotNil(t, setup.Dispatcher)
	require.NotNil(t, setup.NodeBroker)
	require.NotNil(t, setup.MockClientFactory)

	// Add a node and verify client creation
	setup.addTestNode("test-node", 8081)
	client := setup.getNodeClient("test-node", 8081)
	require.NotNil(t, client)
}

func TestPoCRetry(t *testing.T) {
	params := types.EpochParams{
		EpochLength:           100,
		EpochShift:            0,
		EpochMultiplier:       1,
		PocStageDuration:      20,
		PocExchangeDuration:   2,
		PocValidationDelay:    2,
		PocValidationDuration: 10,
	}
	reconciliationConfig := testreconcilialtionConfig(2)
	setup := createIntegrationTestSetup(&reconciliationConfig, &params)

	// Add two nodes
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	node1Client.WithTryLock(t, func() {
		node1Client.InitGenerateError = errors.New("test error")
	})

	var i = params.EpochLength
	setup.transitionChainStateToNextEpoch(i)
	err := setup.simulateBlock(i)
	i++
	require.NoError(t, err)

	waitForAsync(100 * time.Millisecond)

	assertNodeClient(t, NodeClientAssertion{0, 1, 0, 0}, node1Client)
	assertNodeClient(t, NodeClientAssertion{0, 1, 0, 0}, node2Client)
	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_FAILED, n.State.CurrentStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
	})

	for i <= params.EpochLength+int64(reconciliationConfig.PoC.BlockInterval) {
		err = setup.simulateBlock(i)
		require.NoError(t, err)

		i++
	}

	waitForAsync(100 * time.Millisecond)

	// check PoC init generate was retried
	assertNodeClient(t, NodeClientAssertion{0, 2, 0, 0}, node1Client)
	assertNodeClient(t, NodeClientAssertion{0, 1, 0, 0}, node2Client)
	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_FAILED, n.State.CurrentStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
	})

	node1Client.WithTryLock(t, func() {
		node1Client.InitGenerateError = nil
	})

	for i < params.EpochLength+params.GetEndOfPoCStage() {
		err = setup.simulateBlock(i)
		require.NoError(t, err)

		waitForAsync(100 * time.Millisecond)

		i++
	}

	// waitForAsync(100 * time.Millisecond)

	// check only 1 retry happened and then it stopped once we removed the error
	assertNodeClient(t, NodeClientAssertion{0, 3, 0, 0}, node1Client)
	assertNodeClient(t, NodeClientAssertion{0, 1, 0, 0}, node2Client)
	setup.assertNode("node-1", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
	})
	setup.assertNode("node-2", func(n broker.NodeResponse) {
		require.Equal(t, types.HardwareNodeStatus_POC, n.State.CurrentStatus)
		require.Equal(t, broker.PocStatusGenerating, n.State.PocCurrentStatus)
	})
}
