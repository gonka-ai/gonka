package broker

import (
	"testing"
)

func TestSingleNode(t *testing.T) {
	broker := NewBroker()
	node := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan InferenceNode, 2)})
	availableNode := make(chan *InferenceNode, 2)
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
	runningNode := <-availableNode
	if runningNode == nil {
		t.Fatalf("expected node1, got nil")
	}
	if runningNode.Id != node.Id {
		t.Fatalf("expected node1, got: " + runningNode.Id)
	}
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got " + runningNode.Id)
	}
}

func TestNodeRemoval(t *testing.T) {
	broker := NewBroker()
	node := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan InferenceNode, 2)})
	availableNode := make(chan *InferenceNode, 2)
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
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
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got node")
	}
}

func TestModelMismatch(t *testing.T) {
	broker := NewBroker()
	node := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan InferenceNode, 2)})
	availableNode := make(chan *InferenceNode, 2)
	queueMessage(t, broker, LockAvailableNode{"model2", availableNode})
	if <-availableNode != nil {
		t.Fatalf("expected nil, got node1")
	}
}

func TestHighConcurrency(t *testing.T) {
	broker := NewBroker()
	node := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node1",
		MaxConcurrent: 100,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan InferenceNode, 2)})
	availableNode := make(chan *InferenceNode, 2)
	for i := 0; i < 100; i++ {
		queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
		if <-availableNode == nil {
			t.Fatalf("expected node1, got nil")
		}
	}
}

func TestMultipleNodes(t *testing.T) {
	broker := NewBroker()
	node1 := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	node2 := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node2",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node1, make(chan InferenceNode, 2)})
	queueMessage(t, broker, RegisterNode{node2, make(chan InferenceNode, 2)})
	availableNode := make(chan *InferenceNode, 2)
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
	firstNode := <-availableNode
	if firstNode == nil {
		t.Fatalf("expected node1 or node2, got nil")
	}
	println("First Node: " + firstNode.Id)
	if firstNode.Id != node1.Id && firstNode.Id != node2.Id {
		t.Fatalf("expected node1 or node2, got: " + firstNode.Id)
	}
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
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
	broker := NewBroker()
	node := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	queueMessage(t, broker, RegisterNode{node, make(chan InferenceNode, 2)})
	availableNode := make(chan *InferenceNode, 2)
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
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
	queueMessage(t, broker, LockAvailableNode{"model1", availableNode})
	if <-availableNode == nil {
		t.Fatalf("expected node1, got nil")
	}

}

func TestCapacityCheck(t *testing.T) {
	broker := NewBroker()
	node := InferenceNode{
		Host:          "localhost",
		InferencePort: 8080,
		PoCPort:       5000,
		Models:        []string{"model1"},
		Id:            "node1",
		MaxConcurrent: 1,
	}
	if err := broker.QueueMessage(RegisterNode{node, make(chan InferenceNode, 0)}); err == nil {
		t.Fatalf("expected error, got nil")
	}
}
