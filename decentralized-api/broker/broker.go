package broker

import (
	"errors"
	"log/slog"
	"reflect"
)

type Broker struct {
	commands   chan Command
	nodes      map[string]InferenceNode
	nodeStates map[string]*NodeState
}

type NodeState struct {
	LockCount     int    `json:"lock_count"`
	Operational   bool   `json:"operational"`
	FailureReason string `json:"failure_reason"`
}

type NodeResponse struct {
	Node  *InferenceNode `json:"node"`
	State *NodeState     `json:"state"`
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
		slog.Debug("Processing command", "type", reflect.TypeOf(command).String())
		switch command := command.(type) {
		case LockAvailableNode:
			b.lockAvailableNode(command)
		case ReleaseNode:
			b.releaseNode(command)
		case RegisterNode:
			b.registerNode(command)
		case RemoveNode:
			b.removeNode(command)
		case GetNodesCommand:
			b.getNodes(command)
		default:
			slog.Error("Unregistered command type", "type", reflect.TypeOf(command).String())
		}
	}
}

type InvalidCommandError struct {
	Message string
}

func (b *Broker) QueueMessage(command Command) error {
	// Check validity of command. Primarily check all `Response` channels to make sure they
	// support buffering, or else we could end up blocking the broker.
	if command.GetResponseChannelCapacity() == 0 {
		slog.Error("Message queued with unbuffered channel", "command", reflect.TypeOf(command).String())
		return errors.New("response channel must support buffering")
	}
	b.commands <- command
	return nil
}

func (b *Broker) getNodes(command GetNodesCommand) {
	var nodeResponses []NodeResponse
	for _, node := range b.nodes {
		nodeResponses = append(nodeResponses, NodeResponse{
			Node:  &node,
			State: b.nodeStates[node.Id],
		})
	}
	slog.Debug("Got nodes", "size", len(nodeResponses))
	command.Response <- nodeResponses
}

func (b *Broker) registerNode(command RegisterNode) {
	b.nodes[command.Node.Id] = command.Node
	b.nodeStates[command.Node.Id] = &NodeState{
		LockCount:     0,
		Operational:   true,
		FailureReason: "",
	}
	slog.Debug("Registered node", "node", command.Node)
	command.Response <- command.Node
}

func (b *Broker) removeNode(command RemoveNode) {
	if _, ok := b.nodes[command.NodeId]; !ok {
		command.Response <- false
		return
	}
	delete(b.nodes, command.NodeId)
	delete(b.nodeStates, command.NodeId)
	slog.Debug("Removed node", "node_id", command.NodeId)
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
	slog.Debug("Locked node", "node", leastBusyNode)
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
			slog.Error("Node failed", "node_id", command.NodeId, "reason", command.Outcome.GetMessage())
			nodeState.Operational = false
			nodeState.FailureReason = "Inference failed"
		}
	}
	slog.Debug("Released node", "node_id", command.NodeId)
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
			slog.Error("Error releasing node", "error", queueError)
		}
	}()

	return action(node)
}

func (nodeBroker *Broker) GetNodes() ([]NodeResponse, error) {
	response := make(chan []NodeResponse, 2)
	err := nodeBroker.QueueMessage(GetNodesCommand{
		Response: response,
	})
	if err != nil {
		return nil, err
	}
	nodes := <-response

	if nodes == nil {
		return nil, errors.New("Error getting nodes")
	}
	slog.Debug("Got nodes", "size", len(nodes))
	return nodes, nil
}
