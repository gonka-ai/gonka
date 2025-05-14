package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
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

type ModelArgs struct {
	Args []string `json:"args"`
}

type Node struct {
	Host             string               `json:"host"`
	InferenceSegment string               `json:"inference_segment"`
	InferencePort    int                  `json:"inference_port"`
	PoCSegment       string               `json:"poc_segment"`
	PoCPort          int                  `json:"poc_port"`
	Models           map[string]ModelArgs `json:"models"`
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
	LockCount       int                      `json:"lock_count"`
	Operational     bool                     `json:"operational"`
	FailureReason   string                   `json:"failure_reason"`
	TrainingTaskId  uint64                   `json:"training_task_id"`
	Status          types.HardwareNodeStatus `json:"status"`
	StatusTimestamp time.Time                `json:"status_timestamp"`
	IntendedStatus  types.HardwareNodeStatus `json:"intended_status,omitempty"`
	LastStateChange time.Time                `json:"last_state_change"`
}

func (s *NodeState) UpdateStatusAt(time time.Time, status types.HardwareNodeStatus) {
	s.Status = status
	s.StatusTimestamp = time
}

func (s *NodeState) UpdateStatusNow(status types.HardwareNodeStatus) {
	s.Status = status
	s.StatusTimestamp = time.Now()
}

func (s *NodeState) Failure(reason string) {
	s.FailureReason = reason
	s.UpdateStatusNow(types.HardwareNodeStatus_FAILED)
}

func (s *NodeState) IsOperational() bool {
	return s.Status != types.HardwareNodeStatus_FAILED
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

func (b *Broker) LoadNodeToBroker(node *apiconfig.InferenceNodeConfig) {
	if node == nil {
		return
	}

	err := b.QueueMessage(RegisterNode{
		Node:     *node,
		Response: make(chan *apiconfig.InferenceNodeConfig, 2),
	})
	if err != nil {
		logging.Error("Failed to load node to broker", types.Nodes, "error", err)
		panic(err)
	}
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
		case StartPocCommand:
			command.Execute(b)
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

	node := Node{
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
	}

	b.nodes[command.Node.Id] = &NodeWithState{
		Node: node,
		State: NodeState{
			LockCount:       0,
			FailureReason:   "",
			Status:          types.HardwareNodeStatus_UNKNOWN,
			StatusTimestamp: time.Now(),
			IntendedStatus:  types.HardwareNodeStatus_UNKNOWN,
		},
	}
	logging.Debug("Registered node", types.Nodes, "node", command.Node)
	command.Response <- &command.Node
}

func newNodeClient(node *Node) *mlnodeclient.Client {
	return mlnodeclient.NewNodeClient(node.PoCUrl(), node.InferenceUrl())
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
	available := node.State.IsOperational() && node.State.LockCount < node.Node.MaxConcurrent
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
			node.State.Failure("Inference failed")
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

		client := mlnodeclient.NewNodeClient(node.Node.PoCUrl(), node.Node.InferenceUrl())

		err := client.Stop()
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
	if len(a.Models) != len(b.Models) {
		return false
	}

	aModels := make([]string, len(a.Models))
	bModels := make([]string, len(b.Models))
	copy(aModels, a.Models)
	copy(bModels, b.Models)
	sort.Strings(aModels)
	sort.Strings(bModels)

	for i := range aModels {
		if aModels[i] != bModels[i] {
			return false
		}
	}

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
		// TODO: maybe also skip if status is unknown or smth?

		if node.State.Status == node.State.IntendedStatus {
			continue // Node is already in the intended state
		}

		logging.Info("Node state mismatch detected", types.Nodes,
			"node_id", nodeId,
			"current_state", node.State.Status.String(),
			"intended_state", node.State.IntendedStatus.String())

		if node.State.IntendedStatus != types.HardwareNodeStatus_INFERENCE {
			logging.Info("Reconciliation for non-INFERENCE states not yet implemented",
				types.Nodes, "node_id", nodeId)
			continue
		}

		if node.State.IntendedStatus == types.HardwareNodeStatus_INFERENCE &&
			(node.State.Status == types.HardwareNodeStatus_UNKNOWN ||
				node.State.Status == types.HardwareNodeStatus_STOPPED ||
				node.State.Status == types.HardwareNodeStatus_FAILED) {
			b.restoreNodeToInferenceState(node)
		}
	}

	command.Response <- true
}

func (b *Broker) restoreNodeToInferenceState(node *NodeWithState) {
	client := newNodeClient(&node.Node)

	nodeId := node.Node.Id
	logging.Info("Attempting to repair node to INFERENCE state", types.Nodes, "node_id", nodeId)
	err := client.Stop()
	if err != nil {
		logging.Error("Failed to stop node during reconciliation", types.Nodes,
			"node_id", nodeId, "error", err)
		return
	} else {
		node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)
	}

	err = inferenceUp(&node.Node, client)
	if err != nil {
		logging.Error("Failed to bring up inference during reconciliation", types.Nodes, "noeId", nodeId, "error", err)
		return
	} else {
		logging.Info("Successfully repaired node to INFERENCE state", types.Nodes, "nodeId", nodeId)
		node.State.UpdateStatusNow(types.HardwareNodeStatus_INFERENCE)
	}
}

func (b *Broker) setNodesActualStatus(command SetNodesActualStatusCommand) {
	for _, update := range command.StatusUpdates {
		nodeId := update.NodeId
		node, exists := b.nodes[nodeId]
		if !exists {
			logging.Error("Cannot set status: node not found", types.Nodes, "node_id", nodeId)
			continue
		}

		if node.State.StatusTimestamp.After(update.Timestamp) {
			logging.Info("Skipping status update: older than current", types.Nodes, "node_id", nodeId)
			continue
		}

		node.State.UpdateStatusAt(update.Timestamp, update.NewStatus)
		logging.Info("Set actual status for node", types.Nodes,
			"node_id", nodeId, "status", update.NewStatus.String())
	}

	command.Response <- true
}

func nodeStatusQueryWorker(broker *Broker) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		nodes, err := broker.GetNodes()
		if err != nil {
			logging.Error("nodeStatusQueryWorker. Failed to get nodes for status query", types.Nodes, "error", err)
			continue
		}

		statusUpdates := make([]StatusUpdate, 0)

		for _, nodeResp := range nodes {
			queryStatusResult, err := broker.queryNodeStatus(*nodeResp.Node, *nodeResp.State)
			timestamp := time.Now()
			if err != nil {
				logging.Error("nodeStatusQueryWorker. Failed to queue status query command", types.Nodes,
					"nodeId", nodeResp.Node.Id, "error", err)
				continue
			}

			if queryStatusResult.PrevStatus != queryStatusResult.CurrentStatus {
				logging.Info("nodeStatusQueryWorker. Node status changed", types.Nodes,
					"nodeId", nodeResp.Node.Id,
					"prevStatus", queryStatusResult.PrevStatus.String(),
					"currentStatus", queryStatusResult.CurrentStatus.String())

				statusUpdates = append(statusUpdates, StatusUpdate{
					NodeId:     nodeResp.Node.Id,
					PrevStatus: queryStatusResult.PrevStatus,
					NewStatus:  queryStatusResult.CurrentStatus,
					Timestamp:  timestamp,
				})
			}
		}

		err = broker.QueueMessage(SetNodesActualStatusCommand{
			StatusUpdates: statusUpdates,
			Response:      make(chan bool, 2),
		})
		if err != nil {
			logging.Error("nodeStatusQueryWorker. Failed to queue status update command", types.Nodes, "error", err)
			continue
		}
	}
}

type statusQueryResult struct {
	PrevStatus    types.HardwareNodeStatus
	CurrentStatus types.HardwareNodeStatus
}

// Pass by value, because this is supposed to be a readonly function
func (b *Broker) queryNodeStatus(node Node, state NodeState) (*statusQueryResult, error) {
	client := newNodeClient(&node)

	status, err := client.NodeState()

	nodeId := node.Id
	if err != nil {
		logging.Error("queryNodeStatus. Failed to query node status", types.Nodes,
			"nodeId", nodeId, "error", err)
		return nil, err
	}

	prevStatus := state.Status
	currentStatus := toStatus(*status)
	logging.Info("queryNodeStatus. Queried node status", types.Nodes, "nodeId", nodeId, "currentStatus", currentStatus.String(), "prevStatus", prevStatus.String())

	if currentStatus == types.HardwareNodeStatus_INFERENCE {
		ok, err := client.InferenceHealth()
		if !ok || err != nil {
			currentStatus = types.HardwareNodeStatus_FAILED
			logging.Info("queryNodeStatus. Node inference health check failed", types.Nodes, "nodeId", nodeId, "currentStatus", currentStatus.String(), "prevStatus", prevStatus.String(), "err", err)
		}
	}

	return &statusQueryResult{
		PrevStatus:    prevStatus,
		CurrentStatus: currentStatus,
	}, nil
}

func toStatus(response mlnodeclient.StateResponse) types.HardwareNodeStatus {
	switch response.State {
	case mlnodeclient.MlNodeState_POW:
		return types.HardwareNodeStatus_POC
	case mlnodeclient.MlNodeState_INFERENCE:
		return types.HardwareNodeStatus_INFERENCE
	case mlnodeclient.MlNodeState_TRAIN:
		return types.HardwareNodeStatus_TRAINING
	case mlnodeclient.MlNodeState_STOPPED:
		return types.HardwareNodeStatus_STOPPED
	default:
		return types.HardwareNodeStatus_UNKNOWN
	}
}

func (b *Broker) inferenceUpAll(command InferenceUpAllCommand) {
	for _, node := range b.nodes {
		node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE

		client := newNodeClient(&node.Node)

		err := client.Stop()
		if err != nil {
			logging.Error("Failed to stop node for inference up", types.Nodes,
				"node_id", node.Node.Id, "error", err)
			continue
		} else {
			node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)
		}

		err = inferenceUp(&node.Node, client)
		if err != nil {
			logging.Error("Failed to bring up inference", types.Nodes,
				"node_id", node.Node.Id, "error", err)
		} else {
			node.State.UpdateStatusNow(types.HardwareNodeStatus_INFERENCE)
		}
	}

	command.Response <- true
}

func inferenceUp(node *Node, nodeClient *mlnodeclient.Client) error {
	if len(node.Models) == 0 {
		logging.Error("No models found for node, can't inference up", types.Nodes,
			"node_id", node.Id, "error")
		return errors.New("no models found for node, can't inference up")
	}

	model := ""
	var modelArgs []string
	for modelName, args := range node.Models {
		model = modelName
		modelArgs = args.Args
		break
	}

	return nodeClient.InferenceUp(model, modelArgs)
}
