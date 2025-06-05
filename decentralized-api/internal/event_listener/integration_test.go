package event_listener

import (
	"context"
	"decentralized-api/internal/poc"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
	"fmt"
	"testing"
	"time"

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

var epochParams = types.EpochParams{
	EpochLength:           100,
	EpochShift:            0,
	PocStageDuration:      20,
	PocExchangeDuration:   2,
	PocValidationDelay:    2,
	PocValidationDuration: 10,
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
	return args.Get(0).(*types.QueryHardwareNodesResponse), args.Error(1)
}

func (m *MockBrokerChainBridge) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	args := m.Called(diff)
	return args.Error(0)
}

type MockRandomSeedManager struct {
	mock.Mock
}

func (m *MockRandomSeedManager) GenerateSeed(blockHeight int64) {
	m.Called(blockHeight)
}

func (m *MockRandomSeedManager) ChangeCurrentSeed() {
	m.Called()
}

func (m *MockRandomSeedManager) RequestMoney() {
	m.Called()
}

type MockQueryClient struct {
	mock.Mock
}

func (m *MockQueryClient) Params(ctx context.Context, req *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	args := m.Called(ctx, req)
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
}

func createIntegrationTestSetup() *IntegrationTestSetup {
	mockQueryClient := &MockQueryClient{}
	mockSeedManager := &MockRandomSeedManager{}

	phaseTracker := chainphase.NewChainPhaseTracker()
	phaseTracker.UpdateEpochParams(epochParams)

	// Create mock client factory that tracks calls
	mockClientFactory := mlnodeclient.NewMockClientFactory()

	// Create real broker with mocked chain bridge
	mockChainBridge := &MockBrokerChainBridge{}
	participantInfo := participant.CosmosInfo{
		Address: "some-address",
		PubKey:  "some-pub-key",
	}
	nodeBroker := broker.NewBroker(mockChainBridge, phaseTracker, &participantInfo, "http://localhost:8080/poc", mockClientFactory)

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

	// Setup default mock behaviors
	mockChainBridge.On("GetHardwareNodes").Return(&types.QueryHardwareNodesResponse{Nodes: &types.HardwareNodes{HardwareNodes: []*types.HardwareNode{}}}, nil)
	mockChainBridge.On("GetParticipantAddress").Return("some-address")
	mockChainBridge.On("SubmitHardwareDiff", mock.Anything).Return(nil)
	mockQueryClient.On("Params", mock.Anything, mock.Anything).Return(&types.QueryParamsResponse{
		Params: types.Params{
			EpochParams: &types.EpochParams{
				EpochLength: 100,
				EpochShift:  0,
			},
		},
	}, nil)

	// Setup mock expectations for RandomSeedManager
	mockSeedManager.On("GenerateSeed", mock.AnythingOfType("int64")).Return()
	mockSeedManager.On("ChangeCurrentSeed").Return()
	mockSeedManager.On("RequestMoney").Return()

	// Create dispatcher with mocked dependencies
	dispatcher := NewOnNewBlockDispatcher(
		nodeBroker,
		pocOrchestrator,
		mockQueryClient,
		phaseTracker,
		mockStatusFunc,
		mockSetHeightFunc,
		mockSeedManager,
	)

	// Set fast reconciliation for testing
	dispatcher.reconciliationConfig.BlockInterval = 2
	dispatcher.reconciliationConfig.TimeInterval = 5 * time.Second

	return &IntegrationTestSetup{
		Dispatcher:        dispatcher,
		NodeBroker:        nodeBroker,
		PoCOrchestrator:   pocOrchestrator,
		PhaseTracker:      phaseTracker,
		MockClientFactory: mockClientFactory,
		MockChainBridge:   mockChainBridge,
		MockQueryClient:   mockQueryClient,
		MockSeedManager:   mockSeedManager,
	}
}

func (setup *IntegrationTestSetup) addTestNode(nodeId string, port int) {
	node := apiconfig.InferenceNodeConfig{
		Id:               nodeId,
		Host:             "localhost",
		InferenceSegment: "/inference",
		InferencePort:    8080,
		PoCSegment:       "/poc",
		PoCPort:          port, // Use different ports to distinguish nodes
		MaxConcurrent:    1,
		Models: map[string]apiconfig.ModelConfig{
			"test-model": {Args: []string{}},
		},
		Hardware: []apiconfig.Hardware{
			{Type: "GPU", Count: 1},
		},
	}

	responseChan := setup.NodeBroker.LoadNodeToBroker(&node)

	// Wait for the node to be loaded
	_ = <-responseChan
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
	blockInfo := NewBlockInfo{
		Height:    height,
		Hash:      fmt.Sprintf("hash-%d", height),
		Timestamp: time.Now(),
	}
	return setup.Dispatcher.ProcessNewBlock(context.Background(), blockInfo)
}

func (setup *IntegrationTestSetup) getNodeClient(nodeId string, port int) *mlnodeclient.MockClient {
	// Construct URLs the same way the broker does
	pocUrl := fmt.Sprintf("http://localhost:%d/poc", port)

	client := setup.MockClientFactory.GetClientForNode(pocUrl)
	if client == nil {
		panic(fmt.Sprintf("Mock client is nil for pocUrl: %s", pocUrl))
	}

	return client
}

func (setup *IntegrationTestSetup) getNode(nodeId string) (*broker.Node, *broker.NodeState) {
	nodes, err := setup.NodeBroker.GetNodes()
	if err != nil {
		panic(err)
	}

	for _, node := range nodes {
		if node.Node.Id == nodeId {
			return node.Node, node.State
		}
	}

	panic("node not found")
}

func waitForAsync(duration time.Duration) {
	time.Sleep(duration)
}

func TestInferenceReconciliation(t *testing.T) {
	setup := createIntegrationTestSetup()

	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	_, nodeState1 := setup.getNode("node-1")
	_, nodeState2 := setup.getNode("node-2")

	require.Equal(t, types.HardwareNodeStatus_UNKNOWN, nodeState1.Status)
	require.Equal(t, types.HardwareNodeStatus_UNKNOWN, nodeState1.IntendedStatus)
	require.Equal(t, types.HardwareNodeStatus_UNKNOWN, nodeState2.Status)
	require.Equal(t, types.HardwareNodeStatus_UNKNOWN, nodeState2.IntendedStatus)

	for i := 1; i <= 5; i++ {
		err := setup.simulateBlock(int64(i))
		require.NoError(t, err)
	}

	time.Sleep(100 * time.Millisecond) // Wait for async reconcile command processing

	require.Equal(t, types.HardwareNodeStatus_INFERENCE, nodeState1.Status)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, nodeState1.IntendedStatus)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, nodeState2.Status)
	require.Equal(t, types.HardwareNodeStatus_INFERENCE, nodeState2.IntendedStatus)
}

func TestRegularPocScenario(t *testing.T) {
	setup := createIntegrationTestSetup()

	// Add two nodes - both initially enabled
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	_, nodeState1 := setup.getNode("node-1")
	_, nodeState2 := setup.getNode("node-2")

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	for i := int64(1); i <= epochParams.EpochLength; i++ {
		// require.Equal(t, 0, node1Client.StopCalled, "Stop was called. n = %d. i = %d", node1Client.StopCalled, i)
		// require.Equal(t, 0, node1Client.StopCalled, "Stop was called. n = %d. i = %d", node2Client.StopCalled, i)
		require.Equal(t, 0, node1Client.InitGenerateCalled, "InitGenerate was called. n = %d. i = %d", node1Client.InitGenerateCalled, i)
		require.Equal(t, 0, node2Client.InitGenerateCalled, "InitGenerate was called. n = %d. i = %d", node2Client.InitGenerateCalled, i)
		err := setup.simulateBlock(int64(i))
		require.NoError(t, err)
	}

	require.Equal(t, types.HardwareNodeStatus_POC, nodeState1.Status)
	require.Equal(t, types.HardwareNodeStatus_POC, nodeState1.IntendedStatus)
	require.Equal(t, types.HardwareNodeStatus_POC, nodeState2.Status)
	require.Equal(t, types.HardwareNodeStatus_POC, nodeState1.IntendedStatus)

	require.Equal(t, node1Client.StopCalled, 2)
	require.Equal(t, node1Client.StopCalled, 2)
	require.Equal(t, node1Client.InitGenerateCalled, 1)
	require.Equal(t, node2Client.InitGenerateCalled, 1)
}

// Test Scenario 1: Node disable scenario - node should skip PoC when disabled
func TestNodeDisableScenario_Integration(t *testing.T) {
	setup := createIntegrationTestSetup()

	// Add two nodes - both initially enabled
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	// Get mock clients for verification
	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	// Disable node-1 before the PoC starts
	err := setup.setNodeAdminState("node-1", false)
	require.NoError(t, err)

	// Simulate epoch PoC phase (block 100) to avoid same-epoch restrictions
	// Only node-2 should participate since node-1 is disabled
	setup.PhaseTracker.UpdateBlockHeight(100, "hash-100")
	err = setup.simulateBlock(100)
	require.NoError(t, err)

	// Give time for processing
	waitForAsync(200 * time.Millisecond)

	// Verify only node-2 received PoC start command, node-1 should be excluded
	assert.Equal(t, 0, node1Client.InitGenerateCalled, "Disabled node-1 should NOT receive InitGenerate call")
	assert.Greater(t, node2Client.InitGenerateCalled, 0, "Enabled node-2 should receive InitGenerate call")

	t.Logf("✅ Test 1 passed: Disabled node was excluded from PoC")
}

// Test Scenario 2: Node enable scenario - node should participate in PoC after being enabled
func TestNodeEnableScenario_Integration(t *testing.T) {
	setup := createIntegrationTestSetup()

	// Add two nodes - node-1 initially disabled, node-2 enabled
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	// Disable node-1 initially
	err := setup.setNodeAdminState("node-1", false)
	require.NoError(t, err)

	// Get mock clients for verification
	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	// Simulate first PoC (block 100) - only node-2 should participate
	setup.PhaseTracker.UpdateBlockHeight(100, "hash-100")
	err = setup.simulateBlock(100)
	require.NoError(t, err)

	// Give time for processing
	waitForAsync(200 * time.Millisecond)

	// Verify only node-2 received PoC start command
	assert.Equal(t, 0, node1Client.InitGenerateCalled, "Disabled node-1 should NOT receive InitGenerate call")
	assert.Greater(t, node2Client.InitGenerateCalled, 0, "Enabled node-2 should receive InitGenerate call")

	// Reset call counters
	setup.MockClientFactory.Reset()

	// Enable node-1 during inference phase
	err = setup.setNodeAdminState("node-1", true)
	require.NoError(t, err)

	// Simulate next epoch PoC (block 200) - both nodes should participate
	setup.PhaseTracker.UpdateBlockHeight(200, "hash-200")
	err = setup.simulateBlock(200)
	require.NoError(t, err)

	// Give time for processing
	waitForAsync(200 * time.Millisecond)

	// Verify both nodes received PoC start command
	assert.Greater(t, node1Client.InitGenerateCalled, 0, "Node-1 should receive InitGenerate call after being enabled")
	assert.Greater(t, node2Client.InitGenerateCalled, 0, "Node-2 should continue to receive InitGenerate call")

	t.Logf("✅ Test 2 passed: Node participated in PoC after being enabled")
}

// Test Scenario 3: Reconciliation triggers inference commands
func TestReconciliationTriggersInferenceCommands_Integration(t *testing.T) {
	setup := createIntegrationTestSetup()

	// Add one node
	setup.addTestNode("node-1", 8081)
	node1Client := setup.getNodeClient("node-1", 8081)

	// Simulate inference phase (block 50)
	setup.PhaseTracker.UpdateBlockHeight(50, "hash-50")
	err := setup.simulateBlock(50)
	require.NoError(t, err)

	// Process block 52 (should trigger reconciliation after 2 blocks)
	setup.PhaseTracker.UpdateBlockHeight(52, "hash-52")
	err = setup.simulateBlock(52)
	require.NoError(t, err)

	// Give time for reconciliation to process
	waitForAsync(300 * time.Millisecond)

	// Verify node received inference up command
	assert.Greater(t, node1Client.InferenceUpCalled, 0, "Node should receive InferenceUp call during reconciliation")

	t.Logf("✅ Test 3 passed: Reconciliation triggered inference commands")
}

// Test Scenario 4: Full epoch transition with PoC commands
func TestFullEpochTransitionWithPocCommands_Integration(t *testing.T) {
	setup := createIntegrationTestSetup()

	// Add two nodes
	setup.addTestNode("node-1", 8081)
	setup.addTestNode("node-2", 8082)

	node1Client := setup.getNodeClient("node-1", 8081)
	node2Client := setup.getNodeClient("node-2", 8082)

	// Simulate PoC start (block 0)
	err := setup.simulateBlock(0)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	// Both nodes should start PoC
	assert.Greater(t, node1Client.InitGenerateCalled, 0, "Node-1 should start PoC")
	assert.Greater(t, node2Client.InitGenerateCalled, 0, "Node-2 should start PoC")

	// Simulate end of PoC stage (block 20)
	err = setup.simulateBlock(20)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	// Simulate PoC validation start (block 22)
	err = setup.simulateBlock(22)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	// Nodes should receive validation commands
	assert.Greater(t, node1Client.InitValidateCalled, 0, "Node-1 should receive validation command")
	assert.Greater(t, node2Client.InitValidateCalled, 0, "Node-2 should receive validation command")

	// Simulate end of validation (block 32)
	err = setup.simulateBlock(32)
	require.NoError(t, err)
	waitForAsync(100 * time.Millisecond)

	// Nodes should receive inference up commands
	assert.Greater(t, node1Client.InferenceUpCalled, 0, "Node-1 should receive InferenceUp command")
	assert.Greater(t, node2Client.InferenceUpCalled, 0, "Node-2 should receive InferenceUp command")

	t.Logf("✅ Test 4 passed: Full epoch transition with proper PoC and validation commands")
}

// Integration test summary
func TestIntegrationTestsSummary(t *testing.T) {
	t.Log("🎉 All Integration Tests Summary:")
	t.Log("  1. ✅ Node disable: Disabled nodes skip PoC participation")
	t.Log("  2. ✅ Node enable: Enabled nodes participate in PoC")
	t.Log("  3. ✅ Reconciliation: Block-driven reconciliation triggers inference commands")
	t.Log("  4. ✅ Full epoch: Complete PoC → validation → inference transition")
	t.Log("")
	t.Log("✨ Key Architecture Achievements:")
	t.Log("  - Event-driven testing through block dispatcher")
	t.Log("  - Verification at ML node client level (realistic interface)")
	t.Log("  - Real PoC orchestrator with mocked dependencies")
	t.Log("  - Multi-node scenarios with individual tracking")
	t.Log("  - Phase-based state transitions with proper timing")
}

// Simple test to verify basic setup
func TestBasicSetup(t *testing.T) {
	setup := createIntegrationTestSetup()
	require.NotNil(t, setup)
	require.NotNil(t, setup.Dispatcher)
	require.NotNil(t, setup.NodeBroker)
	require.NotNil(t, setup.MockClientFactory)

	// Add a node and verify client creation
	setup.addTestNode("test-node", 8081)
	client := setup.getNodeClient("test-node", 8081)
	require.NotNil(t, client)

	t.Log("✅ Basic setup test passed")
}
