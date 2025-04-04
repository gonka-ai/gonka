package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/productscience/inference/x/inference/types"
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

type Node struct {
	Host          string
	InferencePort int
	PoCPort       int
	Models        []string
	Id            string
	MaxConcurrent int
	NodeNum       uint64
	Hardware      []apiconfig.Hardware
}

func (n *Node) InferenceUrl() string {
	return fmt.Sprintf("http://%s:%d", n.Host, n.InferencePort)
}

func (n *Node) PoCUrl() string {
	return fmt.Sprintf("http://%s:%d", n.Host, n.PoCPort)
}

type NodeWithState struct {
	Node  Node
	State NodeState
}

type NodeState struct {
	LockCount       int                       `json:"lock_count"`
	Operational     bool                      `json:"operational"`
	FailureReason   string                    `json:"failure_reason"`
	TrainingTaskId  uint64                    `json:"training_task_id"`
	Status          types.HardwareNodeStatus  `json:"status"`
	IntendedStatus  *types.HardwareNodeStatus `json:"intended_status,omitempty"`
	LastStateChange time.Time                 `json:"last_state_change"`
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
	go nodeStatusQueryWorker(broker)
	go nodeReconciliationWorker(broker)
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
		case LockNodesForTrainingCommand:
			b.lockNodesForTraining(command)
		case StartTrainingCommand:
			b.startTraining(command)
		case ReconcileNodesCommand:
			b.reconcileNodes(command)
		case SetNodesActualStatusCommand:
			b.setNodesActualStatus(command)
		case InferenceUpAllCommand:
			b.inferenceUpAll(command)
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

	b.nodes[command.Node.Id] = &NodeWithState{
		Node: Node{
			Host:          command.Node.Host,
			InferencePort: command.Node.InferencePort,
			PoCPort:       command.Node.PoCPort,
			Models:        command.Node.Models,
			Id:            command.Node.Id,
			MaxConcurrent: command.Node.MaxConcurrent,
			NodeNum:       curNum,
			Hardware:      command.Node.Hardware,
		},
		State: NodeState{
			LockCount: 0,
			// PRTODO: !!! remove operational? now you have statuses!
			Operational:   true,
			FailureReason: "",
			// FIXME
			// 	PRTODO: !!! it can be different!, query the node for it's status
			Status: types.HardwareNodeStatus_INFERENCE,
		},
	}
	logging.Debug("Registered node", types.Nodes, "node", command.Node)
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
		if nodeAvailable(node, command.Model) {
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
		command.Response <- nil
	} else {
		command.Response <- &leastBusyNode.Node
	}
}

func nodeAvailable(node *NodeWithState, neededModel string) bool {
	available := node.State.Operational && node.State.LockCount < node.Node.MaxConcurrent
	if !available {
		return false
	}
	for _, model := range node.Node.Models {
		if model == neededModel {
			return true
		}
	}
	return false
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

func LockNode[T any](
	b *Broker,
	model string,
	action func(node *Node) (T, error),
) (T, error) {
	var zero T
	nodeChan := make(chan *Node, 2)
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

func (b *Broker) lockNodesForTraining(command LockNodesForTrainingCommand) {
	// PRTODO: implement
	command.Response <- true
}

func (b *Broker) startTraining(command StartTrainingCommand) {
	for nodeId, rank := range command.nodeRanks {
		node, nodeFound := b.nodes[nodeId]
		if !nodeFound {
			logging.Error("Node not found", types.Nodes, "node_id", nodeId)
			command.Response <- false
			return
		}

		client, err := NewNodeClient(&node.Node)
		if err != nil {
			// FIXME: think how this will be retried,
			// 	because you might have started the training on some nodes by this moment
			logging.Error("Error creating node client", types.Nodes, "error", err)
			command.Response <- false
			return
		}

		// TODO: check node status before hand. Maybe it's already doing the training??
		err = client.Stop()
		if err != nil {
			logging.Error("Error stopping training", types.Nodes, "error", err)
			command.Response <- false
			return
		}

		err = client.StartTraining(command.masterNodeAddress, rank, command.worldSize)
		if err != nil {
			logging.Error("Error starting training", types.Nodes, "error", err)
			command.Response <- false
			return
		}
	}

	command.Response <- true
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
	return &types.HardwareNode{
		LocalId:  node.Id,
		Status:   in.State.Status,
		Hardware: hardware,
		Host:     node.Host,
		Port:     strconv.Itoa(node.PoCPort),
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

func nodeReconciliationWorker(broker *Broker) {
	ticker := time.NewTicker(2 * time.Minute) // Reconcile every 2 minutes
	defer ticker.Stop()

	for range ticker.C {
		logging.Debug("Starting node state reconciliation", types.Nodes)
		response := make(chan bool, 1)
		err := broker.QueueMessage(ReconcileNodesCommand{
			Response: response,
		})

		if err != nil {
			logging.Error("Failed to queue reconciliation command", types.Nodes, "error", err)
			continue
		}

		// We don't need to wait for the response here
	}
}

func (b *Broker) reconcileNodes(command ReconcileNodesCommand) {
	for nodeId, node := range b.nodes {
		if node.State.IntendedStatus == nil {
			continue // No intended state set, skip reconciliation
		}

		if node.State.Status == *node.State.IntendedStatus {
			// TODO: check inference is actually alive???
			continue // Node is already in the intended state
		}

		logging.Info("Node state mismatch detected", types.Nodes,
			"node_id", nodeId,
			"current_state", node.State.Status.String(),
			"intended_state", node.State.IntendedStatus.String())

		if *node.State.IntendedStatus != types.HardwareNodeStatus_INFERENCE {
			logging.Info("Reconciliation for non-INFERENCE states not yet implemented",
				types.Nodes, "node_id", nodeId)
			continue
		}

		client, err := NewNodeClient(&node.Node)
		if err != nil {
			logging.Error("Failed to create client for reconciliation", types.Nodes,
				"node_id", nodeId, "error", err)
			continue
		}

		logging.Info("Attempting to repair node to INFERENCE state", types.Nodes, "node_id", nodeId)
		err = client.Stop()
		if err != nil {
			logging.Error("Failed to stop node during reconciliation", types.Nodes,
				"node_id", nodeId, "error", err)
			continue
		}

		node.State.Operational = true
		node.State.FailureReason = ""

		logging.Info("Successfully repaired node to INFERENCE state", types.Nodes, "node_id", nodeId)
	}

	command.Response <- true
}

func (b *Broker) setNodesActualStatus(command SetNodesActualStatusCommand) {
	for nodeId, status := range command.NodeIdToStatus {
		node, exists := b.nodes[nodeId]
		if !exists {
			logging.Error("Cannot set status: node not found", types.Nodes, "node_id", nodeId)
			continue
		}

		node.State.Status = status
		logging.Info("Set actual status for node", types.Nodes,
			"node_id", nodeId, "status", status.String())
	}

	command.Response <- true
}

func nodeStatusQueryWorker(broker *Broker) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		nodes, err := broker.GetNodes()
		if err != nil {
			logging.Error("Failed to get nodes for status query", types.Nodes, "error", err)
			continue
		}

		changedStatuses := make(map[string]types.HardwareNodeStatus)

		for _, nodeResp := range nodes {
			queryStatusResult, err := broker.queryNodeStatus(nodeResp.Node.Id)
			if err != nil {
				logging.Error("Failed to queue status query command", types.Nodes,
					"node_id", nodeResp.Node.Id, "error", err)
				continue
			}

			if queryStatusResult.PrevStatus != queryStatusResult.NewStatus {
				logging.Info("Node status changed", types.Nodes,
					"node_id", nodeResp.Node.Id,
					"prev_status", queryStatusResult.PrevStatus.String(),
					"new_status", queryStatusResult.NewStatus.String())

				changedStatuses[nodeResp.Node.Id] = queryStatusResult.NewStatus
			}
		}

		err = broker.QueueMessage(SetNodesActualStatusCommand{
			NodeIdToStatus: changedStatuses,
			Response:       make(chan bool, 2),
		})
		if err != nil {
			logging.Error("Failed to queue status update command", types.Nodes, "error", err)
			continue
		}
	}
}

type statusQueryResult struct {
	PrevStatus types.HardwareNodeStatus
	NewStatus  types.HardwareNodeStatus
}

func (b *Broker) queryNodeStatus(nodeId string) (*statusQueryResult, error) {
	node, exists := b.nodes[nodeId]
	if !exists {
		logging.Error("Cannot query status: node not found", types.Nodes, "node_id", nodeId)
		return nil, errors.New("node not found")
	}

	client, err := NewNodeClient(&node.Node)
	if err != nil {
		logging.Error("Failed to create client for node status query", types.Nodes,
			"node_id", nodeId, "error", err)
		return nil, err
	}

	status, err := client.NodeState()
	if err != nil {
		logging.Error("Failed to query node status", types.Nodes,
			"node_id", nodeId, "error", err)
		return nil, err
	}

	prevStatus := node.State.Status
	newStatus := toStatus(*status)

	return &statusQueryResult{
		PrevStatus: prevStatus,
		NewStatus:  newStatus,
	}, nil
}

func toStatus(response StateResponse) types.HardwareNodeStatus {
	switch response.State {
	case MlNodeState_POW:
		return types.HardwareNodeStatus_POC
	case MlNodeState_INFERENCE:
		return types.HardwareNodeStatus_INFERENCE
	case MlNodeState_TRAIN:
		return types.HardwareNodeStatus_TRAINING
	case MlNodeState_STOPPED:
		return types.HardwareNodeStatus_STOPPED
	default:
		return types.HardwareNodeStatus_UNKNOWN
	}
}

func (b *Broker) inferenceUpAll(command InferenceUpAllCommand) {
	for _, node := range b.nodes {
		client, err := NewNodeClient(&node.Node)
		if err != nil {
			logging.Error("Failed to create client for inference up", types.Nodes,
				"node_id", node.Node.Id, "error", err)
			continue
		}

		err = client.Stop()
		if err != nil {
			logging.Error("Failed to stop node for inference up", types.Nodes,
				"node_id", node.Node.Id, "error", err)
			continue
		} else {
			status := types.HardwareNodeStatus_STOPPED
			node.State.IntendedStatus = &status
			node.State.Status = types.HardwareNodeStatus_STOPPED
		}

		err = client.InferenceUp()
		if err != nil {
			logging.Error("Failed to bring up inference", types.Nodes,
				"node_id", node.Node.Id, "error", err)
		} else {
			intendedStatus := types.HardwareNodeStatus_INFERENCE
			node.State.IntendedStatus = &intendedStatus
			node.State.Status = types.HardwareNodeStatus_INFERENCE
		}
	}

	command.Response <- true
}
