package broker

import (
	"errors"
	"log"
	"reflect"
)

type Broker struct {
	commands   chan Command
	nodes      map[string]InferenceNode
	nodeStates map[string]*NodeState
}

type NodeState struct {
	LockCount     int
	Operational   bool
	FailureReason string
}

func NewBroker() *Broker {
	broker := &Broker{
		commands:   make(chan Command, 100),
		nodes:      make(map[string]InferenceNode),
		nodeStates: make(map[string]*NodeState),
	}

	go broker.processCommands()
	return broker
}

func (b *Broker) processCommands() {
	for command := range b.commands {
		println("processing command of type " + reflect.TypeOf(command).String())
		switch command := command.(type) {
		case LockAvailableNode:
			b.lockAvailableNode(command)
		case ReleaseNode:
			b.releaseNode(command)
		case RegisterNode:
			b.registerNode(command)
		case RemoveNode:
			b.removeNode(command)
		}
	}

	println("Done?")
}

type InvalidCommandError struct {
	Message string
}

func (b *Broker) QueueMessage(command Command) error {
	// Check validity of command. Primarily check all `Response` channels to make sure they
	// support buffering, or else we could end up blocking the broker.
	if command.GetResponseChannelCapacity() == 0 {
		return errors.New("response channel must support buffering")
	}
	b.commands <- command
	return nil
}

func (b *Broker) registerNode(command RegisterNode) {
	b.nodes[command.Node.Id] = command.Node
	b.nodeStates[command.Node.Id] = &NodeState{
		LockCount:     0,
		Operational:   true,
		FailureReason: "",
	}
	command.Response <- command.Node
}

func (b *Broker) removeNode(command RemoveNode) {
	if _, ok := b.nodes[command.NodeId]; !ok {
		command.Response <- false
		return
	}
	delete(b.nodes, command.NodeId)
	delete(b.nodeStates, command.NodeId)
	command.Response <- true
}

func (b *Broker) lockAvailableNode(command LockAvailableNode) {
	var leastBusyNode *InferenceNode = nil

	for _, node := range b.nodes {
		if nodeAvailable(b, node, command.Model) {
			if leastBusyNode == nil || b.nodeStates[node.Id].LockCount < b.nodeStates[leastBusyNode.Id].LockCount {
				leastBusyNode = &node
			}
		}
	}
	if leastBusyNode != nil {
		state := b.nodeStates[leastBusyNode.Id]
		state.LockCount++
	}
	command.Response <- leastBusyNode
}

func nodeAvailable(b *Broker, node InferenceNode, neededModel string) bool {
	available := b.nodeStates[node.Id].Operational && b.nodeStates[node.Id].LockCount < node.MaxConcurrent
	if !available {
		return false
	}
	for _, model := range node.Models {
		if model == neededModel {
			return true
		}
	}
	return false
}

func (b *Broker) releaseNode(command ReleaseNode) {
	if nodeState, ok := b.nodeStates[command.NodeId]; !ok {
		command.Response <- false
		return
	} else {
		nodeState.LockCount--
		if !command.Outcome.IsSuccess() {
			nodeState.Operational = false
			nodeState.FailureReason = "Inference failed"
		}
	}

	command.Response <- true
}

func LockNode[T any](
	b *Broker,
	model string,
	action func(node *InferenceNode) (T, error),
) (T, error) {
	var zero T
	nodeChan := make(chan *InferenceNode, 2)
	err := b.QueueMessage(LockAvailableNode{
		Model:    model,
		Response: nodeChan,
	})
	if err != nil {
		return zero, err
	}
	node := <-nodeChan
	if node == nil {
		return zero, errors.New("No nodes available")
	}

	defer func() {
		queueError := b.QueueMessage(ReleaseNode{
			NodeId: node.Id,
			Outcome: InferenceSuccess{
				Response: nil,
			},
			Response: make(chan bool, 2),
		})

		if queueError != nil {
			log.Printf("Error releasing node = %v", queueError)
		}
	}()

	return action(node)
}
