package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
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

// BrokerChainBridge defines the interface for the broker to interact with the blockchain.
// This abstraction allows for easier testing and isolates the broker from the specifics
// of the cosmos client implementation.
type BrokerChainBridge interface {
	GetHardwareNodes() (*types.QueryHardwareNodesResponse, error)
	SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error
}

type BrokerChainBridgeImpl struct {
	client cosmosclient.CosmosMessageClient
}

func NewBrokerChainBridgeImpl(client cosmosclient.CosmosMessageClient) BrokerChainBridge {
	return &BrokerChainBridgeImpl{client: client}
}

func (b *BrokerChainBridgeImpl) GetHardwareNodes() (*types.QueryHardwareNodesResponse, error) {
	queryClient := b.client.NewInferenceQueryClient()
	req := &types.QueryHardwareNodesRequest{
		Participant: b.client.GetAddress(),
	}
	return queryClient.HardwareNodes(*b.client.GetContext(), req)
}

func (b *BrokerChainBridgeImpl) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	_, err := b.client.SendTransaction(diff)
	return err
}

type Broker struct {
	commands            chan Command
	nodes               map[string]*NodeWithState
	curMaxNodesNum      atomic.Uint64
	chainBridge         BrokerChainBridge
	nodeWorkGroup       *NodeWorkGroup
	phaseTracker        *chainphase.ChainPhaseTracker
	participantInfo     participant.CurrenParticipantInfo
	callbackUrl         string
	mlNodeClientFactory mlnodeclient.ClientFactory
}

const (
	PoCBatchesPath = "/v1/poc-batches"
)

func GetPocBatchesCallbackUrl(callbackUrl string) string {
	return fmt.Sprintf("%s"+PoCBatchesPath, callbackUrl)
}

func GetPocValidateCallbackUrl(callbackUrl string) string {
	// For now the URl is the same, the node inference server appends "/validated" to the URL
	//  or "/generated" (in case of init-generate)
	return fmt.Sprintf("%s"+PoCBatchesPath, callbackUrl)
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

// AdminState tracks administrative enable/disable status
type AdminState struct {
	Enabled bool   `json:"enabled"`
	Epoch   uint64 `json:"epoch"`
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
	AdminState      AdminState               `json:"admin_state"`
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

// ShouldBeOperational checks if node should be operational based on admin state and current epoch
func (s *NodeState) ShouldBeOperational(currentEpoch uint64, currentPhase types.EpochPhase) bool {
	if !s.AdminState.Enabled {
		// Disabled nodes stop after their epoch ends
		return s.AdminState.Epoch >= currentEpoch
	}

	// Enabled nodes wait for inference phase if enabled during PoC
	if s.AdminState.Epoch == currentEpoch && currentPhase != types.InferencePhase {
		return false
	}

	return true
}

type NodeResponse struct {
	Node  *Node      `json:"node"`
	State *NodeState `json:"state"`
}

func NewBroker(chainBridge BrokerChainBridge, phaseTracker *chainphase.ChainPhaseTracker, participantInfo participant.CurrenParticipantInfo, callbackUrl string, clientFactory mlnodeclient.ClientFactory) *Broker {
	broker := &Broker{
		commands:            make(chan Command, 10000),
		nodes:               make(map[string]*NodeWithState),
		chainBridge:         chainBridge,
		phaseTracker:        phaseTracker,
		participantInfo:     participantInfo,
		callbackUrl:         callbackUrl,
		mlNodeClientFactory: clientFactory,
	}

	// Initialize NodeWorkGroup
	broker.nodeWorkGroup = NewNodeWorkGroup()

	go broker.processCommands()
	go nodeSyncWorker(broker)
	// Reconciliation is now triggered by OnNewBlockDispatcher
	// go nodeReconciliationWorker(broker)
	go nodeStatusQueryWorker(broker)
	return broker
}

func (b *Broker) LoadNodeToBroker(node *apiconfig.InferenceNodeConfig) chan *apiconfig.InferenceNodeConfig {
	if node == nil {
		return nil
	}

	responseChan := make(chan *apiconfig.InferenceNodeConfig, 2)
	err := b.QueueMessage(RegisterNode{
		Node:     *node,
		Response: responseChan,
	})
	if err != nil {
		logging.Error("Failed to load node to broker", types.Nodes, "error", err)
		panic(err)
	}

	return responseChan
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
			command.Execute(b)
		case ReconcileNodesCommand:
			b.reconcileNodes(command)
		case SetNodesActualStatusCommand:
			b.setNodesActualStatus(command)
		case SetNodeAdminStateCommand:
			b.setNodeAdminState(command)
		case InferenceUpAllCommand:
			b.inferenceUpAll(command)
		case StartPocCommand:
			command.Execute(b)
		case InitValidateCommand:
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

	// Get current epoch from phase tracker
	var currentEpoch uint64
	if b.phaseTracker != nil {
		currentEpoch = b.phaseTracker.GetCurrentEpoch()
	}

	nodeWithState := &NodeWithState{
		Node: node,
		State: NodeState{
			LockCount:       0,
			FailureReason:   "",
			Status:          types.HardwareNodeStatus_UNKNOWN,
			StatusTimestamp: time.Now(),
			IntendedStatus:  types.HardwareNodeStatus_UNKNOWN,
			AdminState: AdminState{
				Enabled: true,
				Epoch:   currentEpoch,
			},
		},
	}

	b.nodes[command.Node.Id] = nodeWithState

	// Create and register a worker for this node
	client := b.NewNodeClient(&node)
	worker := NewNodeWorkerWithClient(command.Node.Id, nodeWithState, client)
	b.nodeWorkGroup.AddWorker(command.Node.Id, worker)

	logging.Debug("Registered node", types.Nodes, "node", command.Node)
	command.Response <- &command.Node
}

func (b *Broker) NewNodeClient(node *Node) mlnodeclient.MLNodeClient {
	return b.mlNodeClientFactory.CreateClient(node.PoCUrl(), node.InferenceUrl())
}

func (b *Broker) removeNode(command RemoveNode) {
	// Remove the worker first (it will wait for pending jobs)
	b.nodeWorkGroup.RemoveWorker(command.NodeId)

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
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()
	for _, node := range b.nodes {
		if b.nodeAvailable(node, command.Model, command.Version, epochPhaseInfo.Epoch, epochPhaseInfo.Phase) {
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

func (b *Broker) nodeAvailable(node *NodeWithState, neededModel string, version string, currentEpoch uint64, currentPhase types.EpochPhase) bool {
	available := node.State.IsOperational() && node.State.LockCount < node.Node.MaxConcurrent
	if !available {
		return false
	}

	// Check admin state using provided epoch and phase
	if !node.State.ShouldBeOperational(currentEpoch, currentPhase) {
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
	resp, err := b.chainBridge.GetHardwareNodes()
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
		if err = b.chainBridge.SubmitHardwareDiff(&diff); err != nil {
			logging.Error("[sync nodes] Error submitting diff", types.Nodes, "error", err)
		}
	}
}

func (b *Broker) calculateNodesDiff(chainNodesMap map[string]*types.HardwareNode) types.MsgSubmitHardwareDiff {
	var diff types.MsgSubmitHardwareDiff
	diff.Creator = b.participantInfo.GetAddress()

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

func GetNodeIntendedStatusForPhase(phase types.EpochPhase) types.HardwareNodeStatus {
	switch phase {
	case types.InferencePhase:
		return types.HardwareNodeStatus_INFERENCE
	case types.PoCGeneratePhase:
		return types.HardwareNodeStatus_POC
	case types.PoCGenerateWindDownPhase:
		return types.HardwareNodeStatus_STOPPED // FIXME: hanlde me
	case types.PoCValidatePhase:
		return types.HardwareNodeStatus_STOPPED // FIXME: hanlde me
	case types.PoCValidateWindDownPhase:
		return types.HardwareNodeStatus_STOPPED // FIXME: hanlde me
	}
	return types.HardwareNodeStatus_STOPPED
}
func (b *Broker) reconcileNodes(command ReconcileNodesCommand) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()
	currentEpoch := epochPhaseInfo.Epoch
	currentPhase := epochPhaseInfo.Phase
	currentBlockHeight := epochPhaseInfo.BlockHeight

	intendedNodeStatusForPhase := GetNodeIntendedStatusForPhase(epochPhaseInfo.Phase)
	logging.Info("Reconciling nodes based on current phase", types.Nodes,
		"phase", currentPhase,
		"epoch", currentEpoch,
		"blockHeigh", currentBlockHeight,
		"intendedNodeStatusForPhase", intendedNodeStatusForPhase.String())

	// Collect nodes that need reconciliation
	needsReconciliation := make(map[string]NodeWorkerCommand)

	for nodeId, node := range b.nodes {
		// Skip nodes in training state
		if node.State.IntendedStatus == types.HardwareNodeStatus_TRAINING {
			continue
		}

		// Check if node should be operational based on admin state
		shouldBeOperational := node.State.ShouldBeOperational(currentEpoch, currentPhase)

		if !shouldBeOperational {
			// Node should be stopped
			if node.State.Status != types.HardwareNodeStatus_STOPPED {
				logging.Info("Node should be stopped due to admin state", types.Nodes,
					"node_id", nodeId,
					"admin_enabled", node.State.AdminState.Enabled,
					"admin_epoch", node.State.AdminState.Epoch,
					"current_epoch", currentEpoch)
				needsReconciliation[nodeId] = StopNodeCommand{}
			}
			continue
		}

		// Update intended status based on phase
		node.State.IntendedStatus = intendedNodeStatusForPhase

		// Check if reconciliation is needed
		if node.State.Status != node.State.IntendedStatus {
			logging.Info("Node state mismatch detected", types.Nodes,
				"node_id", nodeId,
				"current_state", node.State.Status.String(),
				"intended_state", node.State.IntendedStatus.String())

			switch node.State.IntendedStatus {
			case types.HardwareNodeStatus_INFERENCE:
				needsReconciliation[nodeId] = InferenceUpNodeCommand{}
			case types.HardwareNodeStatus_POC:
				pocParams := b.phaseTracker.GetPoCParameters()
				if pocParams.IsInPoC && pocParams.PoCStartHeight > 0 {
					totalNodes := len(b.nodes)
					needsReconciliation[nodeId] = StartPoCNodeCommand{
						BlockHeight: pocParams.PoCStartHeight,
						BlockHash:   pocParams.PoCStartHash,
						PubKey:      b.participantInfo.GetPubKey(),
						CallbackUrl: GetPocBatchesCallbackUrl(b.callbackUrl),
						TotalNodes:  totalNodes,
					}
				} else {
					logging.Warn("Cannot reconcile to PoC: missing PoC parameters", types.Nodes,
						"node_id", nodeId)
					needsReconciliation[nodeId] = &NoOpNodeCommand{Message: "POC reconciliation: missing parameters"}
				}
			case types.HardwareNodeStatus_STOPPED:
				needsReconciliation[nodeId] = StopNodeCommand{}
			default:
				logging.Info("Reconciliation for state not yet implemented", types.Nodes,
					"node_id", nodeId, "intended_state", node.State.IntendedStatus.String())
				needsReconciliation[nodeId] = &NoOpNodeCommand{Message: "Unknown state reconciliation"}
			}
		}
	}

	if len(needsReconciliation) == 0 {
		logging.Debug("All nodes are in their intended state", types.Nodes)
		command.Response <- true
		return
	}

	// Limit concurrent reconciliations and execute
	const maxConcurrentReconciliations = 10
	submitted := 0
	failed := 0
	processed := 0

	for nodeId, cmd := range needsReconciliation {
		if processed >= maxConcurrentReconciliations {
			logging.Info("Limiting reconciliation batch", types.Nodes,
				"total_needs_reconciliation", len(needsReconciliation),
				"batch_size", maxConcurrentReconciliations)
			break
		}
		if worker, exists := b.nodeWorkGroup.GetWorker(nodeId); exists {
			if worker.Submit(cmd) {
				submitted++
			} else {
				failed++
				logging.Error("Failed to submit reconciliation command to worker", types.Nodes,
					"node_id", nodeId, "reason", "queue full")
			}
		} else {
			logging.Error("Worker not found for reconciliation", types.Nodes, "node_id", nodeId)
			failed++ // Count as failed if worker doesn't exist
		}
		processed++
	}

	// Wait for submitted commands
	for nodeId := range needsReconciliation {
		if worker, exists := b.nodeWorkGroup.GetWorker(nodeId); exists {
			// Check if this cmd was actually submitted (could be tricky if map iteration order matters)
			// For simplicity, just wait on all workers that had commands.
			// A better way would be to track submitted workers.
			worker.wg.Wait()
		}
	}

	logging.Info("Reconciliation batch completed", types.Nodes,
		"submitted", submitted, "failed", failed, "batch_size", processed)

	command.Response <- true
}

func (b *Broker) reconcileNodeToInference(node *NodeWithState) error {
	if node.State.Status == types.HardwareNodeStatus_UNKNOWN ||
		node.State.Status == types.HardwareNodeStatus_STOPPED ||
		node.State.Status == types.HardwareNodeStatus_FAILED {

		b.restoreNodeToInferenceState(node)
	}
	return nil
}

func (b *Broker) restoreNodeToInferenceState(node *NodeWithState) {
	client := b.NewNodeClient(&node.Node)

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

func (b *Broker) setNodeAdminState(command SetNodeAdminStateCommand) {
	node, exists := b.nodes[command.NodeId]
	if !exists {
		command.Response <- fmt.Errorf("node not found: %s", command.NodeId)
		return
	}

	// Get current epoch
	var currentEpoch uint64
	if b.phaseTracker != nil {
		currentEpoch = b.phaseTracker.GetCurrentEpoch()
	}

	// Update admin state
	node.State.AdminState.Enabled = command.Enabled
	node.State.AdminState.Epoch = currentEpoch

	logging.Info("Updated node admin state", types.Nodes,
		"node_id", command.NodeId,
		"enabled", command.Enabled,
		"epoch", currentEpoch)

	command.Response <- nil
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
	client := b.NewNodeClient(&node)

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
	// Update intended status for all nodes
	for _, node := range b.nodes {
		node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	}

	// Create a single command instance
	cmd := InferenceUpNodeCommand{}

	// Execute inference up on all nodes in parallel
	submitted, failed := b.nodeWorkGroup.ExecuteOnAll(cmd)

	logging.Info("InferenceUpAllCommand completed", types.Nodes,
		"submitted", submitted, "failed", failed, "total", len(b.nodes))

	command.Response <- true
}

func inferenceUp(node *Node, nodeClient mlnodeclient.MLNodeClient) error {
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
