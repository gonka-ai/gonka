package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slog"
)

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

	// Create a mock config manager with default version
	mockConfigManager := &apiconfig.ConfigManager{}
	return NewBroker(nil, phaseTracker, participantInfo, "", mlnodeclient.NewMockClientFactory(), mockConfigManager)
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

	time.Sleep(10 * time.Millisecond)
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
		Version:       "v3.0.8",
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

	// Test the immediate refresh directly - this should call stop on the old client immediately
	worker.RefreshClientImmediate("v3.0.8", "v3.1.0")

	// Give some time for the async stop call to complete
	time.Sleep(50 * time.Millisecond)

	// Verify stop was called on the old client
	assert.Greater(t, mockClient.StopCalled, initialStopCalled, "Stop should have been called on old client")

	// Test the full immediate refresh flow again
	previousStopCalled := mockClient.StopCalled

	// Call immediate refresh again with different version
	worker.RefreshClientImmediate("v3.1.0", "v3.2.0")

	// Give some time for async stop calls to complete
	time.Sleep(50 * time.Millisecond)

	// Should have called stop again
	assert.Greater(t, mockClient.StopCalled, previousStopCalled, "Stop should have been called again during second refresh")
}

func TestManualClientRefreshTrigger(t *testing.T) {
	// Test the manual trigger mechanism
	broker := NewTestBroker()

	// Test triggering the manual refresh
	broker.TriggerClientRefresh()

	// Give some time for the trigger to be processed
	time.Sleep(10 * time.Millisecond)

	// Verify it doesn't block on multiple triggers
	broker.TriggerClientRefresh()
	broker.TriggerClientRefresh()

	// Should not hang or panic
	assert.True(t, true, "Manual triggers should not block or panic")
}
