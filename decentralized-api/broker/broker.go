package broker

import (
	"context"
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
	"sync"
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
	GetBlockHash(height int64) (string, error)
}

type BrokerChainBridgeImpl struct {
	client       cosmosclient.CosmosMessageClient
	chainNodeUrl string
}

func NewBrokerChainBridgeImpl(client cosmosclient.CosmosMessageClient, chainNodeUrl string) BrokerChainBridge {
	return &BrokerChainBridgeImpl{client: client, chainNodeUrl: chainNodeUrl}
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

func (b *BrokerChainBridgeImpl) GetBlockHash(height int64) (string, error) {
	client, err := cosmosclient.NewRpcClient(b.chainNodeUrl)
	if err != nil {
		return "", err
	}

	block, err := client.Block(context.Background(), &height)
	if err != nil {
		return "", err
	}

	return block.Block.Hash().String(), err
}

type Broker struct {
	highPriorityCommands chan Command
	lowPriorityCommands  chan Command
	nodes                map[string]*NodeWithState
	mu                   sync.RWMutex
	curMaxNodesNum       atomic.Uint64
	chainBridge          BrokerChainBridge
	nodeWorkGroup        *NodeWorkGroup
	phaseTracker         *chainphase.ChainPhaseTracker
	participantInfo      participant.CurrenParticipantInfo
	callbackUrl          string
	mlNodeClientFactory  mlnodeclient.ClientFactory
	reconcileTrigger     chan struct{}
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
	IntendedStatus     types.HardwareNodeStatus `json:"intended_status"`
	CurrentStatus      types.HardwareNodeStatus `json:"current_status"`
	ReconcileInfo      *ReconcileInfo           `json:"reconcile_info,omitempty"`
	cancelInFlightTask func()

	PocIntendedStatus PocStatus `json:"poc_intended_status"`
	PocCurrentStatus  PocStatus `json:"poc_current_status"`

	TrainingTask *TrainingTaskPayload

	LockCount       int        `json:"lock_count"`
	FailureReason   string     `json:"failure_reason"`
	StatusTimestamp time.Time  `json:"status_timestamp"`
	LastStateChange time.Time  `json:"last_state_change"`
	AdminState      AdminState `json:"admin_state"`
}

type TrainingTaskPayload struct {
	Id             uint64         `json:"id"`
	MasterNodeAddr string         `json:"master_node_addr"`
	NodeRanks      map[string]int `json:"node_ranks"`
	WorldSize      int            `json:"world_size"`
}

type ReconcileInfo struct {
	Status         types.HardwareNodeStatus
	PocStatus      PocStatus
	TrainingTaskId uint64
}

func (s *NodeState) UpdateStatusAt(time time.Time, status types.HardwareNodeStatus) {
	s.CurrentStatus = status
	s.StatusTimestamp = time
}

func (s *NodeState) UpdateStatusWithPocStatusNow(status types.HardwareNodeStatus, pocStatus PocStatus) {
	s.CurrentStatus = status
	s.PocCurrentStatus = pocStatus
	s.StatusTimestamp = time.Now()
}

func (s *NodeState) UpdateStatusNow(status types.HardwareNodeStatus) {
	s.CurrentStatus = status
	s.StatusTimestamp = time.Now()
}

func (s *NodeState) Failure(reason string) {
	s.FailureReason = reason
	s.UpdateStatusNow(types.HardwareNodeStatus_FAILED)
}

func (s *NodeState) IsOperational() bool {
	return s.CurrentStatus != types.HardwareNodeStatus_FAILED
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
		highPriorityCommands: make(chan Command, 100),
		lowPriorityCommands:  make(chan Command, 10000),
		nodes:                make(map[string]*NodeWithState),
		chainBridge:          chainBridge,
		phaseTracker:         phaseTracker,
		participantInfo:      participantInfo,
		callbackUrl:          callbackUrl,
		mlNodeClientFactory:  clientFactory,
		reconcileTrigger:     make(chan struct{}, 1),
	}

	// Initialize NodeWorkGroup
	broker.nodeWorkGroup = NewNodeWorkGroup()

	go broker.processCommands()
	go nodeSyncWorker(broker)
	// Reconciliation is now triggered by OnNewBlockDispatcher
	// go nodeReconciliationWorker(broker)
	go nodeStatusQueryWorker(broker)
	go broker.reconcilerLoop()
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
	for {
		select {
		case command := <-b.highPriorityCommands:
			b.executeCommand(command)
		default:
			select {
			case command := <-b.highPriorityCommands:
				b.executeCommand(command)
			case command := <-b.lowPriorityCommands:
				b.executeCommand(command)
			}
		}
	}
}

func (b *Broker) executeCommand(command Command) {
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
	case SetNodesActualStatusCommand:
		b.setNodesActualStatus(command)
	case SetNodeAdminStateCommand:
		command.Execute(b)
	case InferenceUpAllCommand:
		command.Execute(b)
	case StartPocCommand:
		command.Execute(b)
	case InitValidateCommand:
		command.Execute(b)
	case UpdateNodeResultCommand:
		command.Execute(b)
	default:
		logging.Error("Unregistered command type", types.Nodes, "type", reflect.TypeOf(command).String())
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

	switch command.(type) {
	case StartPocCommand, InitValidateCommand, InferenceUpAllCommand, UpdateNodeResultCommand, SetNodesActualStatusCommand, SetNodeAdminStateCommand, RegisterNode, RemoveNode, StartTrainingCommand, LockNodesForTrainingCommand, SyncNodesCommand:
		b.highPriorityCommands <- command
	default:
		b.lowPriorityCommands <- command
	}
	return nil
}

func (b *Broker) getNodes(command GetNodesCommand) {
	b.mu.RLock()
	defer b.mu.RUnlock()
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
		currentEpoch = b.phaseTracker.GetCurrentEpochState().CurrentEpoch.EpochIndex
	}

	nodeWithState := &NodeWithState{
		Node: node,
		State: NodeState{
			IntendedStatus:    types.HardwareNodeStatus_UNKNOWN,
			CurrentStatus:     types.HardwareNodeStatus_UNKNOWN,
			ReconcileInfo:     nil,
			PocIntendedStatus: PocStatusIdle,
			PocCurrentStatus:  PocStatusIdle,
			LockCount:         0,
			FailureReason:     "",
			StatusTimestamp:   time.Now(),
			AdminState: AdminState{
				Enabled: true,
				Epoch:   currentEpoch,
			},
		},
	}

	func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.nodes[command.Node.Id] = nodeWithState

		// Create and register a worker for this node
		client := b.NewNodeClient(&node)
		worker := NewNodeWorkerWithClient(command.Node.Id, nodeWithState, client, b)
		b.nodeWorkGroup.AddWorker(command.Node.Id, worker)
	}()

	logging.Debug("Registered node", types.Nodes, "node", command.Node)
	command.Response <- &command.Node
}

func (b *Broker) NewNodeClient(node *Node) mlnodeclient.MLNodeClient {
	return b.mlNodeClientFactory.CreateClient(node.PoCUrl(), node.InferenceUrl())
}

func (b *Broker) removeNode(command RemoveNode) {
	// Remove the worker first (it will wait for pending jobs)
	b.nodeWorkGroup.RemoveWorker(command.NodeId)

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.nodes[command.NodeId]; !ok {
		command.Response <- false
		return
	}
	delete(b.nodes, command.NodeId)
	logging.Debug("Removed node", types.Nodes, "node_id", command.NodeId)
	command.Response <- true
}

func (b *Broker) lockAvailableNode(command LockAvailableNode) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var leastBusyNode *NodeWithState = nil
	epochState := b.phaseTracker.GetCurrentEpochState()
	for _, node := range b.nodes {
		if b.nodeAvailable(node, command.Model, command.Version, epochState.CurrentEpoch.EpochIndex, epochState.CurrentPhase) {
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
	available := node.State.CurrentStatus == types.HardwareNodeStatus_INFERENCE &&
		node.State.ReconcileInfo == nil &&
		node.State.LockCount < node.Node.MaxConcurrent
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
	b.mu.Lock()
	defer b.mu.Unlock()
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

	chainNodesMap := make(map[string]*types.HardwareNode)
	for _, node := range resp.Nodes.HardwareNodes {
		chainNodesMap[node.LocalId] = node
	}

	b.mu.RLock()
	nodesCopy := make(map[string]*NodeWithState, len(b.nodes))
	for id, node := range b.nodes {
		nodesCopy[id] = node
	}
	b.mu.RUnlock()

	logging.Info("[sync nodes] Local nodes", types.Nodes, "size", len(nodesCopy))

	diff := b.calculateNodesDiff(chainNodesMap, nodesCopy)

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

func (b *Broker) calculateNodesDiff(chainNodesMap map[string]*types.HardwareNode, localNodes map[string]*NodeWithState) types.MsgSubmitHardwareDiff {
	var diff types.MsgSubmitHardwareDiff
	diff.Creator = b.participantInfo.GetAddress()

	for id, localNode := range localNodes {
		localHWNode := convertInferenceNodeToHardwareNode(localNode)

		chainNode, exists := chainNodesMap[id]
		if !exists {
			diff.NewOrModified = append(diff.NewOrModified, localHWNode)
		} else if !areHardwareNodesEqual(localHWNode, chainNode) {
			diff.NewOrModified = append(diff.NewOrModified, localHWNode)
		}
	}

	for id, chainNode := range chainNodesMap {
		if _, exists := localNodes[id]; !exists {
			diff.Removed = append(diff.Removed, chainNode)
		}
	}
	return diff
}

func (b *Broker) lockNodesForTraining(command LockNodesForTrainingCommand) {
	b.mu.Lock()
	defer b.mu.Unlock()
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

	modelNames := make([]string, 0)
	for model := range node.Models {
		modelNames = append(modelNames, model)
	}

	// sort models names to make sure they will be in same order every time
	sort.Strings(modelNames)

	return &types.HardwareNode{
		LocalId:  node.Id,
		Status:   in.State.CurrentStatus,
		Hardware: hardware,
		Models:   modelNames,
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

type pocParams struct {
	startPoCBlockHeight int64
	startPoCBlockHash   string
}

const reconciliationInterval = 30 * time.Second

func (b *Broker) TriggerReconciliation() {
	select {
	case b.reconcileTrigger <- struct{}{}:
	default:
	}
}

func (b *Broker) reconcilerLoop() {
	ticker := time.NewTicker(reconciliationInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.reconcileTrigger:
			epochPhaseInfo := b.phaseTracker.GetCurrentEpochState()
			if !epochPhaseInfo.IsSynced {
				logging.Warn("Reconciliation triggered while epoch phase info is not synced", types.Nodes, "blockHeight", epochPhaseInfo.CurrentBlock.Height)
				continue
			}

			logging.Info("Reconciliation triggered manually", types.Nodes, "blockHeight", epochPhaseInfo.CurrentBlock.Height)
			b.reconcile(*epochPhaseInfo)
		case <-ticker.C:
			epochPhaseInfo := b.phaseTracker.GetCurrentEpochState()
			if !epochPhaseInfo.IsSynced {
				logging.Warn("Reconciliation triggered while epoch phase info is not synced", types.Nodes, "blockHeight", epochPhaseInfo.CurrentBlock.Height)
				continue
			}

			logging.Info("Periodic reconciliation triggered", types.Nodes, "blockHeight", epochPhaseInfo.CurrentBlock.Height)
			b.reconcile(*epochPhaseInfo)
		}
	}
}

func (b *Broker) reconcile(epochState chainphase.EpochState) {
	blockHeight := epochState.CurrentBlock.Height

	// Phase 1: Cancel outdated tasks
	nodesToCancel := make(map[string]func())
	b.mu.RLock()
	for id, node := range b.nodes {
		if node.State.ReconcileInfo != nil &&
			(node.State.ReconcileInfo.Status != node.State.IntendedStatus ||
				node.State.ReconcileInfo.PocStatus != node.State.PocIntendedStatus) {
			if node.State.cancelInFlightTask != nil {
				nodesToCancel[id] = node.State.cancelInFlightTask
			}
		}
	}
	b.mu.RUnlock()

	for id, cancel := range nodesToCancel {
		logging.Info("Cancelling outdated task for node", types.Nodes, "node_id", id, "blockHeight", blockHeight)
		cancel()
		b.mu.Lock()
		if node, ok := b.nodes[id]; ok {
			node.State.ReconcileInfo = nil
			node.State.cancelInFlightTask = nil
		}
		b.mu.Unlock()
	}

	nodesToDispatch := make(map[string]*NodeWithState)
	b.mu.RLock()
	for id, node := range b.nodes {
		isStable := node.State.ReconcileInfo == nil
		if !isStable {
			continue
		}

		// Condition: The primary or PoC intended state does not match the current state.
		if node.State.IntendedStatus != node.State.CurrentStatus || node.State.PocIntendedStatus != node.State.PocCurrentStatus {
			nodeCopy := *node
			nodesToDispatch[id] = &nodeCopy
		}
	}
	b.mu.RUnlock()

	currentPoCParams, pocParamsErr := b.prefetchPocParams(epochState, nodesToDispatch, blockHeight)

	for id, node := range nodesToDispatch {
		// Re-check conditions under write lock to prevent races
		b.mu.Lock()
		currentNode, ok := b.nodes[id]
		if !ok ||
			(currentNode.State.IntendedStatus == currentNode.State.CurrentStatus && (currentNode.State.CurrentStatus != types.HardwareNodeStatus_POC || currentNode.State.PocIntendedStatus == currentNode.State.PocCurrentStatus)) ||
			currentNode.State.ReconcileInfo != nil {
			b.mu.Unlock()
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		intendedStatusCopy := currentNode.State.IntendedStatus
		pocIntendedStatusCopy := currentNode.State.PocIntendedStatus
		currentNode.State.ReconcileInfo = &ReconcileInfo{
			Status:    intendedStatusCopy,
			PocStatus: pocIntendedStatusCopy,
		}
		currentNode.State.cancelInFlightTask = cancel

		worker, exists := b.nodeWorkGroup.GetWorker(id)
		b.mu.Unlock()

		if !exists {
			logging.Error("Worker not found for reconciliation", types.Nodes, "node_id", id, "blockHeight", blockHeight)
			cancel() // Cancel context if worker doesn't exist
			b.mu.Lock()
			if nodeToClean, ok := b.nodes[id]; ok {
				nodeToClean.State.ReconcileInfo = nil
				nodeToClean.State.cancelInFlightTask = nil
			}
			b.mu.Unlock()
			continue
		}

		// Create and dispatch the command
		cmd := b.getCommandForState(&node.State, currentPoCParams, pocParamsErr, len(nodesToDispatch))
		if cmd != nil {
			logging.Info("Dispatching reconciliation command", types.Nodes,
				"node_id", id, "target_status", node.State.IntendedStatus, "target_poc_status", node.State.PocIntendedStatus, "blockHeight", blockHeight)
			if !worker.Submit(ctx, cmd) {
				logging.Error("Failed to submit reconciliation command", types.Nodes, "node_id", id, "blockHeight", blockHeight)
				cancel()
				b.mu.Lock()
				if nodeToClean, ok := b.nodes[id]; ok {
					nodeToClean.State.ReconcileInfo = nil
					nodeToClean.State.cancelInFlightTask = nil
				}
				b.mu.Unlock()
			}
		} else {
			logging.Info("No valid command for reconciliation, cleaning up", types.Nodes, "node_id", id)
			cancel()
			b.mu.Lock()
			if nodeToClean, ok := b.nodes[id]; ok {
				nodeToClean.State.ReconcileInfo = nil
				nodeToClean.State.cancelInFlightTask = nil
			}
			b.mu.Unlock()
		}
	}
}

func (b *Broker) prefetchPocParams(epochState chainphase.EpochState, nodesToDispatch map[string]*NodeWithState, blockHeight int64) (*pocParams, error) {
	needsPocParams := false
	for _, node := range nodesToDispatch {
		if node.State.IntendedStatus == types.HardwareNodeStatus_POC {
			if node.State.PocIntendedStatus == PocStatusGenerating || node.State.PocIntendedStatus == PocStatusValidating {
				needsPocParams = true
			}
		}
	}

	if needsPocParams {
		currentPoCParams, pocParamsErr := b.queryCurrentPoCParams(int64(epochState.CurrentEpoch.PocStartBlockHeight))
		if pocParamsErr != nil {
			logging.Error("Failed to query PoC Generation parameters, skipping PoC reconciliation", types.Nodes, "error", pocParamsErr, "blockHeight", blockHeight)
		}
		return currentPoCParams, pocParamsErr
	} else {
		return nil, nil
	}
}

func (b *Broker) getCommandForState(nodeState *NodeState, pocGenParams *pocParams, pocGenErr error, totalNodes int) NodeWorkerCommand {
	switch nodeState.IntendedStatus {
	case types.HardwareNodeStatus_INFERENCE:
		return InferenceUpNodeCommand{}
	case types.HardwareNodeStatus_POC:
		switch nodeState.PocIntendedStatus {
		case PocStatusGenerating:
			if pocGenParams != nil && pocGenParams.startPoCBlockHeight > 0 {
				return StartPoCNodeCommand{
					BlockHeight: pocGenParams.startPoCBlockHeight,
					BlockHash:   pocGenParams.startPoCBlockHash,
					PubKey:      b.participantInfo.GetPubKey(),
					CallbackUrl: GetPocBatchesCallbackUrl(b.callbackUrl),
					TotalNodes:  totalNodes,
				}
			}
			logging.Error("Cannot create StartPoCNodeCommand: missing PoC parameters", types.Nodes, "error", pocGenErr)
			return nil
		case PocStatusValidating:
			if pocGenParams != nil && pocGenParams.startPoCBlockHeight > 0 {
				return InitValidateNodeCommand{
					BlockHeight: pocGenParams.startPoCBlockHeight,
					BlockHash:   pocGenParams.startPoCBlockHash,
					PubKey:      b.participantInfo.GetPubKey(),
					CallbackUrl: GetPocValidateCallbackUrl(b.callbackUrl),
					TotalNodes:  totalNodes,
				}
			}
			logging.Error("Cannot create InitValidateNodeCommand: missing PoC parameters", types.Nodes, "error", pocGenErr)
			return nil
		default:
			return nil // No action for other phases if status is POC
		}
	case types.HardwareNodeStatus_STOPPED:
		return StopNodeCommand{}
	case types.Training:
		if nodeState.TrainingTask == nil {
			logging.Error("Training task ID is nil, cannot create StartTrainingCommand", types.Nodes)
			return nil
		}
		return StartTrainingNodeCommand{
			TaskId:         nodeState.TrainingTask.Id,
			Participant:    b.participantInfo.GetAddress(),
			MasterNodeAddr: nodeState.TrainingTask.MasterNodeAddr,
			NodeRanks:      nodeState.TrainingTask.NodeRanks,
			WorldSize:      nodeState.TrainingTask.WorldSize,
		}
	default:
		logging.Info("Reconciliation for state not yet implemented", types.Nodes,
			"intended_state", nodeState.IntendedStatus.String())
		return nil
	}
}

func (b *Broker) queryCurrentPoCParams(epochPoCStartHeight int64) (*pocParams, error) {
	hash, err := b.chainBridge.GetBlockHash(epochPoCStartHeight)
	if err != nil {
		logging.Error("Failed to query PoC start block hash", types.Nodes, "height", epochPoCStartHeight, "error", err)
		return nil, err
	}
	return &pocParams{
		startPoCBlockHeight: epochPoCStartHeight,
		startPoCBlockHash:   hash,
	}, nil
}

func (b *Broker) setNodesActualStatus(command SetNodesActualStatusCommand) {
	b.mu.Lock()
	defer b.mu.Unlock()
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
	client := b.NewNodeClient(&node)

	status, err := client.NodeState(context.Background())

	nodeId := node.Id
	if err != nil {
		logging.Error("queryNodeStatus. Failed to query node status", types.Nodes,
			"nodeId", nodeId, "error", err)
		return nil, err
	}

	prevStatus := state.CurrentStatus
	currentStatus := toStatus(*status)
	logging.Info("queryNodeStatus. Queried node status", types.Nodes, "nodeId", nodeId, "currentStatus", currentStatus.String(), "prevStatus", prevStatus.String())

	if currentStatus == types.HardwareNodeStatus_INFERENCE {
		ok, err := client.InferenceHealth(context.Background())
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

func (b *Broker) updateNodeResult(command UpdateNodeResultCommand) {
	b.mu.Lock()
	defer b.mu.Unlock()

	node, exists := b.nodes[command.NodeId]
	if !exists {
		logging.Warn("Received result for unknown node", types.Nodes, "node_id", command.NodeId)
		command.Response <- false
		return
	}

	// For logging and debugging purposes
	blockHeight := b.phaseTracker.GetCurrentEpochState().CurrentBlock.Height

	// Critical safety check
	if node.State.ReconcileInfo == nil ||
		node.State.ReconcileInfo.Status != command.Result.OriginalTarget ||
		(node.State.ReconcileInfo.Status == types.HardwareNodeStatus_POC && node.State.ReconcileInfo.PocStatus != command.Result.OriginalPocTarget) {
		logging.Info("Ignoring stale result for node", types.Nodes,
			"node_id", command.NodeId,
			"original_target", command.Result.OriginalTarget,
			"original_poc_target", command.Result.OriginalPocTarget,
			"current_reconciling_target", node.State.ReconcileInfo.Status,
			"current_reconciling_poc_target", node.State.ReconcileInfo.PocStatus,
			"blockHeight", blockHeight)
		command.Response <- false
		return
	}

	// Update state
	logging.Info("Finalizing state transition for node", types.Nodes,
		"node_id", command.NodeId,
		"from_status", node.State.CurrentStatus,
		"to_status", command.Result.FinalStatus,
		"from_poc_status", node.State.PocCurrentStatus,
		"to_poc_status", command.Result.FinalPocStatus,
		"succeeded", command.Result.Succeeded,
		"blockHeight", blockHeight)

	node.State.UpdateStatusWithPocStatusNow(command.Result.FinalStatus, command.Result.FinalPocStatus)
	node.State.ReconcileInfo = nil
	node.State.cancelInFlightTask = nil
	if !command.Result.Succeeded {
		node.State.FailureReason = command.Result.Error
	}

	command.Response <- true
}
