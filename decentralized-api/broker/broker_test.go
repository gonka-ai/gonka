package broker

import (
	"decentralized-api/apiconfig"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slog"
	"testing"
)

func TestSingleNode(t *testing.T) {
	broker := NewBroker(nil)
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan apiconfig.InferenceNodeConfig, 2)})
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

func TestNodeRemoval(t *testing.T) {
	broker := NewBroker(nil)
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan apiconfig.InferenceNodeConfig, 2)})
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
	broker := NewBroker(nil)
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan apiconfig.InferenceNodeConfig, 2)})
	availableNode := make(chan *Node, 2)
	queueMessage(t, broker, LockAvailableNode{"model2", "", false, availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got node1")
	}
}

func TestHighConcurrency(t *testing.T) {
	broker := NewBroker(nil)
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 100,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan apiconfig.InferenceNodeConfig, 2)})
	availableNode := make(chan *Node, 2)
	for i := 0; i < 100; i++ {
		queueMessage(t, broker, LockAvailableNode{"model1", "", false, availableNode})
		if <-availableNode == nil {
			t.Fatalf("expected node1, got nil")
		}
	}
}

func TestVersionFiltering(t *testing.T) {
	broker := NewBroker(nil)
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
	queueMessage(t, broker, RegisterNode{v1node, make(chan apiconfig.InferenceNodeConfig, 2)})
	queueMessage(t, broker, RegisterNode{novNode, make(chan apiconfig.InferenceNodeConfig, 2)})
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
	broker := NewBroker(nil)
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
	queueMessage(t, broker, RegisterNode{node1, make(chan apiconfig.InferenceNodeConfig, 2)})
	queueMessage(t, broker, RegisterNode{node2, make(chan apiconfig.InferenceNodeConfig, 2)})
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
	broker := NewBroker(nil)
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan apiconfig.InferenceNodeConfig, 2)})
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
	broker := NewBroker(nil)
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
	queueMessage(t, broker, RegisterNode{node, make(chan apiconfig.InferenceNodeConfig, 2)})
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
	broker := NewBroker(nil)
	node := apiconfig.InferenceNodeConfig{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        map[string]apiconfig.ModelConfig{"model1": {Args: make([]string, 0)}},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	if err := broker.QueueMessage(RegisterNode{node, make(chan apiconfig.InferenceNodeConfig, 0)}); err == nil {
		t.Fatalf("expected error, got nil")
	}
}
