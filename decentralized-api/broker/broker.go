package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"errors"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"reflect"
	"sort"
	"sync/atomic"
	"time"
)

/*
enum HardwareNodeStatus {
UNKNOWN = 0;
INFERENCE = 1;
POC = 2;
TRAINING = 3;
}
*/

type Broker struct {
	commands       chan Command
	nodes          map[string]*NodeWithState
	curMaxNodesNum atomic.Uint64
	client         cosmosclient.CosmosMessageClient
}

type ModelArgs struct {
	Args []string
}

type Node struct {
	Host             string `json:"host"`
	InferenceSegment string `json:"inference_segment"`
	InferencePort    int    `json:"inference_port"`
	PoCSegment       string `json:"poc_segment"`
	PoCPort          int    `json:"poc_port"`
	Models           map[string]ModelArgs
	Id               string               `json:"id"`
	MaxConcurrent    int                  `json:"max_concurrent"`
	NodeNum          uint64               `json:"node_num"`
	Hardware         []apiconfig.Hardware `json:"hardware"`
	Version          string               `json:"version"`
}

func (n *Node) InferenceUrl() string {
	return fmt.Sprintf("http://%s:%d%s", n.Host, n.InferencePort, n.InferenceSegment)
}

func (n *Node) PoCUrl() string {
	return fmt.Sprintf("http://%s:%d%s", n.Host, n.PoCPort, n.PoCSegment)
}

type NodeWithState struct {
	Node  Node
	State NodeState
}

type NodeState struct {
	LockCount      int                      `json:"lock_count"`
	Operational    bool                     `json:"operational"`
	FailureReason  string                   `json:"failure_reason"`
	TrainingTaskId uint64                   `json:"training_task_id"`
	Status         types.HardwareNodeStatus `json:"status"`
}

type NodeResponse struct {
	Node  *Node      `json:"node"`
	State *NodeState `json:"state"`
}

func NewBroker(client cosmosclient.CosmosMessageClient) *Broker {
	broker := &Broker{
		commands: make(chan Command, 100),
		nodes:    make(map[string]*NodeWithState),
		client:   client,
	}

	go broker.processCommands()
	go nodeSyncWorker(broker)
	return broker
}

func nodeSyncWorker(broker *Broker) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		logging.Debug("Syncing nodes", types.Nodes)
		if err := broker.QueueMessage(NewSyncNodesCommand()); err != nil {
			logging.Error("Error syncing nodes", types.Nodes, "error", err)
		}
	}
}

func (b *Broker) processCommands() {
	for command := range b.commands {
		logging.Debug("Processing command", types.Nodes, "type", reflect.TypeOf(command).String())
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
		case SyncNodesCommand:
			b.syncNodes()
		default:
			logging.Error("Unregistered command type", types.Nodes, "type", reflect.TypeOf(command).String())
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
		logging.Error("Message queued with unbuffered channel", types.Nodes, "command", reflect.TypeOf(command).String())
		return errors.New("response channel must support buffering")
	}
	b.commands <- command
	return nil
}

func (b *Broker) getNodes(command GetNodesCommand) {
	var nodeResponses []NodeResponse
	for _, node := range b.nodes {
		nodeResponses = append(nodeResponses, NodeResponse{
			Node:  &node.Node,
			State: &node.State,
		})
	}
	logging.Debug("Got nodes", types.Nodes, "size", len(nodeResponses))
	command.Response <- nodeResponses
}

func (b *Broker) registerNode(command RegisterNode) {
	b.curMaxNodesNum.Add(1)
	curNum := b.curMaxNodesNum.Load()

	models := make(map[string]ModelArgs)
	for model, config := range command.Node.Models {
		models[model] = ModelArgs{Args: config.Args}
	}

	b.nodes[command.Node.Id] = &NodeWithState{
		Node: Node{
			Host:             command.Node.Host,
			InferenceSegment: command.Node.InferenceSegment,
			InferencePort:    command.Node.InferencePort,
			PoCSegment:       command.Node.PoCSegment,
			PoCPort:          command.Node.PoCPort,
			Models:           models,
			Id:               command.Node.Id,
			MaxConcurrent:    command.Node.MaxConcurrent,
			NodeNum:          curNum,
			Hardware:         command.Node.Hardware,
			Version:          command.Node.Version,
		},
		State: NodeState{Operational: true},
	}
	logging.Debug("Registered node", types.Nodes, "node", b.nodes[command.Node.Id])
	command.Response <- command.Node
}

func (b *Broker) removeNode(command RemoveNode) {
	if _, ok := b.nodes[command.NodeId]; !ok {
		command.Response <- false
		return
	}
	delete(b.nodes, command.NodeId)
	logging.Debug("Removed node", types.Nodes, "node_id", command.NodeId)
	command.Response <- true
}

func (b *Broker) lockAvailableNode(command LockAvailableNode) {
	var leastBusyNode *NodeWithState = nil

	for _, node := range b.nodes {
		if nodeAvailable(node, command.Model, command.Version) {
			if leastBusyNode == nil || node.State.LockCount < leastBusyNode.State.LockCount {
				leastBusyNode = node
			}
		}
	}

	if leastBusyNode != nil {
		leastBusyNode.State.LockCount++
	}
	logging.Debug("Locked node", types.Nodes, "node", leastBusyNode)
	if leastBusyNode == nil {
		if command.AcceptEarlierVersion {
			b.lockAvailableNode(
				LockAvailableNode{
					Model:                command.Model,
					Response:             command.Response,
					AcceptEarlierVersion: false,
				},
			)
		} else {
			command.Response <- nil
		}
	} else {
		command.Response <- &leastBusyNode.Node
	}
}

func nodeAvailable(node *NodeWithState, neededModel string, version string) bool {
	available := node.State.Operational && node.State.LockCount < node.Node.MaxConcurrent
	if !available {
		return false
	}
	if version != "" && node.Node.Version != version {
		return false
	}

	_, found := node.Node.Models[neededModel]
	if !found {
		logging.Info("Node does not have neededModel", types.Nodes, "node_id", node.Node.Id, "neededModel", neededModel)
	} else {
		logging.Info("Node has neededModel", types.Nodes, "node_id", node.Node.Id, "neededModel", neededModel)
	}
	return found
}

func (b *Broker) releaseNode(command ReleaseNode) {
	if node, ok := b.nodes[command.NodeId]; !ok {
		command.Response <- false
		return
	} else {
		node.State.LockCount--
		if !command.Outcome.IsSuccess() {
			logging.Error("Node failed", types.Nodes, "node_id", command.NodeId, "reason", command.Outcome.GetMessage())
			node.State.Operational = false
			node.State.FailureReason = "Inference failed"
		}
	}
	logging.Debug("Released node", types.Nodes, "node_id", command.NodeId)
	command.Response <- true
}

var ErrNoNodesAvailable = errors.New("no nodes available")

func LockNode[T any](
	b *Broker,
	model string,
	version string,
	action func(node *Node) (T, error),
) (T, error) {
	var zero T
	nodeChan := make(chan *Node, 2)
	err := b.QueueMessage(LockAvailableNode{
		Model:                model,
		Response:             nodeChan,
		Version:              version,
		AcceptEarlierVersion: true,
	})
	if err != nil {
		return zero, err
	}
	node := <-nodeChan
	if node == nil {
		return zero, ErrNoNodesAvailable
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
			logging.Error("Error releasing node", types.Nodes, "error", queueError)
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
	logging.Debug("Got nodes", types.Nodes, "size", len(nodes))
	return nodes, nil
}

func (b *Broker) syncNodes() {
	queryClient := b.client.NewInferenceQueryClient()

	req := &types.QueryHardwareNodesRequest{
		Participant: b.client.GetAddress(),
	}
	resp, err := queryClient.HardwareNodes(*b.client.GetContext(), req)
	if err != nil {
		logging.Error("[sync nodes]. Error getting nodes", types.Nodes, "error", err)
		return
	}
	logging.Info("[sync nodes] Fetched chain nodes", types.Nodes, "size", len(resp.Nodes.HardwareNodes))
	logging.Info("[sync nodes] Local nodes", types.Nodes, "size", len(b.nodes))

	chainNodesMap := make(map[string]*types.HardwareNode)
	for _, node := range resp.Nodes.HardwareNodes {
		chainNodesMap[node.LocalId] = node
	}

	diff := b.calculateNodesDiff(chainNodesMap)

	logging.Info("[sync nodes] Hardware diff computed", types.Nodes, "diff", diff)

	if (diff.Removed == nil || len(diff.Removed) == 0) && (diff.NewOrModified == nil || len(diff.NewOrModified) == 0) {
		logging.Info("[sync nodes] No diff to submit", types.Nodes)
	} else {
		logging.Info("[sync nodes] Submitting diff", types.Nodes)
		if _, err = b.client.SendTransaction(&diff); err != nil {
			logging.Error("[sync nodes] Error submitting diff", types.Nodes, "error", err)
		}
	}
}

func (b *Broker) calculateNodesDiff(chainNodesMap map[string]*types.HardwareNode) types.MsgSubmitHardwareDiff {
	var diff types.MsgSubmitHardwareDiff
	diff.Creator = b.client.GetAddress()

	for id, localNode := range b.nodes {
		localHWNode := convertInferenceNodeToHardwareNode(localNode)

		chainNode, exists := chainNodesMap[id]
		if !exists {
			diff.NewOrModified = append(diff.NewOrModified, localHWNode)
		} else if !areHardwareNodesEqual(localHWNode, chainNode) {
			diff.NewOrModified = append(diff.NewOrModified, localHWNode)
		}
	}

	for id, chainNode := range chainNodesMap {
		if _, exists := b.nodes[id]; !exists {
			diff.Removed = append(diff.Removed, chainNode)
		}
	}
	return diff
}

// convertInferenceNodeToHardwareNode converts a local InferenceNode into a HardwareNode.
func convertInferenceNodeToHardwareNode(in *NodeWithState) *types.HardwareNode {
	node := in.Node
	hardware := make([]*types.Hardware, 0, len(node.Hardware))
	for _, hw := range node.Hardware {
		hardware = append(hardware, &types.Hardware{
			Type:  hw.Type,
			Count: hw.Count,
		})
	}

	modelsNames := make([]string, 0)
	for model := range node.Models {
		modelsNames = append(modelsNames, model)
	}

	// sort models names to make sure they will be in same order every time
	sort.Strings(modelsNames)

	return &types.HardwareNode{
		LocalId:  node.Id,
		Status:   in.State.Status,
		Hardware: hardware,
		Models:   modelsNames,
	}
}

// areHardwareNodesEqual performs a field-by-field comparison between two HardwareNodes.
func areHardwareNodesEqual(a, b *types.HardwareNode) bool {
	// Compare each field that determines whether the node has changed.
	if a.LocalId != b.LocalId {
		return false
	}
	if a.Status != b.Status {
		return false
	}
	if len(a.Hardware) != len(b.Hardware) {
		return false
	}

	if !hardwareEquals(a, b) {
		return false
	}

	return true
}

func hardwareEquals(a *types.HardwareNode, b *types.HardwareNode) bool {
	aHardware := make([]*types.Hardware, len(a.Hardware))
	bHardware := make([]*types.Hardware, len(b.Hardware))
	copy(aHardware, a.Hardware)
	copy(bHardware, b.Hardware)

	sort.Slice(aHardware, func(i, j int) bool {
		if aHardware[i].Type == aHardware[j].Type {
			return aHardware[i].Count < aHardware[j].Count
		}
		return aHardware[i].Type < aHardware[j].Type
	})
	sort.Slice(bHardware, func(i, j int) bool {
		if bHardware[i].Type == bHardware[j].Type {
			return bHardware[i].Count < bHardware[j].Count
		}
		return bHardware[i].Type < bHardware[j].Type
	})

	for i := range aHardware {
		if aHardware[i].Type != bHardware[i].Type || aHardware[i].Count != bHardware[i].Count {
			return false
		}
	}

	return true
}
