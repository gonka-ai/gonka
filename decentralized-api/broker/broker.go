package broker

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"decentralized-api/participant"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
	GetGovernanceModels() (*types.QueryModelsAllResponse, error)
	GetCurrentEpochGroupData() (*types.QueryCurrentEpochGroupDataResponse, error)
	GetEpochGroupDataByModelId(pocHeight uint64, modelId string) (*types.QueryGetEpochGroupDataResponse, error)
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
		Participant: b.client.GetAccountAddress(),
	}
	return queryClient.HardwareNodes(b.client.GetContext(), req)
}

func (b *BrokerChainBridgeImpl) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	_, err := b.client.SendTransactionAsyncNoRetry(diff)
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

func (b *BrokerChainBridgeImpl) GetGovernanceModels() (*types.QueryModelsAllResponse, error) {
	queryClient := b.client.NewInferenceQueryClient()
	req := &types.QueryModelsAllRequest{}
	return queryClient.ModelsAll(b.client.GetContext(), req)
}

func (b *BrokerChainBridgeImpl) GetCurrentEpochGroupData() (*types.QueryCurrentEpochGroupDataResponse, error) {
	queryClient := b.client.NewInferenceQueryClient()
	req := &types.QueryCurrentEpochGroupDataRequest{}
	return queryClient.CurrentEpochGroupData(b.client.GetContext(), req)
}

func (b *BrokerChainBridgeImpl) GetEpochGroupDataByModelId(epochIndex uint64, modelId string) (*types.QueryGetEpochGroupDataResponse, error) {
	queryClient := b.client.NewInferenceQueryClient()
	req := &types.QueryGetEpochGroupDataRequest{
		EpochIndex: epochIndex,
		ModelId:    modelId,
	}
	return queryClient.EpochGroupData(b.client.GetContext(), req)
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
	lastEpochIndex       uint64
	lastEpochPhase       types.EpochPhase
	statusQueryTrigger   chan statusQuerySignal
	configManager        *apiconfig.ConfigManager
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
}

func (n *Node) InferenceUrl() string {
	return fmt.Sprintf("http://%s:%d%s", n.Host, n.InferencePort, n.InferenceSegment)
}

func (n *Node) InferenceUrlWithVersion(version string) string {
	if version == "" {
		return n.InferenceUrl()
	}
	return fmt.Sprintf("http://%s:%d/%s%s", n.Host, n.InferencePort, version, n.InferenceSegment)
}

func (n *Node) PoCUrl() string {
	return fmt.Sprintf("http://%s:%d%s", n.Host, n.PoCPort, n.PoCSegment)
}

func (n *Node) PoCUrlWithVersion(version string) string {
	if version == "" {
		return n.PoCUrl()
	}
	return fmt.Sprintf("http://%s:%d/%s%s", n.Host, n.PoCPort, version, n.PoCSegment)
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

	TrainingTask *TrainingTaskPayload `json:"training_task,omitempty"`

	LockCount       int        `json:"lock_count"`
	FailureReason   string     `json:"failure_reason"`
	StatusTimestamp time.Time  `json:"status_timestamp"`
	AdminState      AdminState `json:"admin_state"`

	// Epoch-specific data, populated from the chain
	EpochModels  map[string]types.Model      `json:"epoch_models"`
	EpochMLNodes map[string]types.MLNodeInfo `json:"epoch_ml_nodes"`
}

func (s NodeState) MarshalJSON() ([]byte, error) {
	type Alias NodeState
	return json.Marshal(&struct {
		IntendedStatus string `json:"intended_status"`
		CurrentStatus  string `json:"current_status"`
		Alias
	}{
		IntendedStatus: s.IntendedStatus.String(),
		CurrentStatus:  s.CurrentStatus.String(),
		Alias:          (Alias)(s),
	})
}

type TrainingTaskPayload struct {
	Id             uint64         `json:"id"`
	MasterNodeAddr string         `json:"master_node_addr"`
	NodeRanks      map[string]int `json:"node_ranks"`
	WorldSize      int            `json:"world_size"`
}

type ReconcileInfo struct {
	Status         types.HardwareNodeStatus `json:"status"`
	PocStatus      PocStatus                `json:"poc_status"`
	TrainingTaskId uint64                   `json:"training_task_id"`
}

func (s *NodeState) UpdateStatusAt(time time.Time, status types.HardwareNodeStatus) {
	s.CurrentStatus = status
	s.StatusTimestamp = time
}

func (s *NodeState) UpdateStatusWithPocStatusNow(status types.HardwareNodeStatus, pocStatus PocStatus) {
	s.UpdateStatusWithPocStatusAt(time.Now(), status, pocStatus)
}

func (s *NodeState) UpdateStatusWithPocStatusAt(time time.Time, status types.HardwareNodeStatus, pocStatus PocStatus) {
	s.CurrentStatus = status
	s.PocCurrentStatus = pocStatus
	s.StatusTimestamp = time
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
func (s *NodeState) ShouldBeOperational(latestEpoch uint64, currentPhase types.EpochPhase) bool {
	return ShouldBeOperational(s.AdminState, latestEpoch, currentPhase)
}

// ShouldContinueInference checks if node should continue inference service
// based on its POC_SLOT timeslot allocation. Returns true if POC_SLOT is set to true
// for any model supported by this node.
func (s *NodeState) ShouldContinueInference() bool {
	// Check if any MLNode for this node has POC_SLOT set to true
	for modelId, mlNodeInfo := range s.EpochMLNodes {
		if len(mlNodeInfo.TimeslotAllocation) > 1 && mlNodeInfo.TimeslotAllocation[1] { // index 1 = POC_SLOT
			logging.Debug("Node should continue inference service based on POC_SLOT allocation", types.PoC,
				"model_id", modelId,
				"poc_slot", mlNodeInfo.TimeslotAllocation[1])
			return true
		}
	}
	return false
}

func ShouldBeOperational(adminState AdminState, latestEpoch uint64, currentPhase types.EpochPhase) bool {
	if adminState.Enabled {
		if latestEpoch > adminState.Epoch {
			return true
		} else { // latestEpoch == adminState.Epoch
			return currentPhase == types.InferencePhase
		}
	} else {
		return adminState.Epoch >= latestEpoch
	}
}

type NodeResponse struct {
	Node  Node      `json:"node"`
	State NodeState `json:"state"`
}

func NewBroker(chainBridge BrokerChainBridge, phaseTracker *chainphase.ChainPhaseTracker, participantInfo participant.CurrenParticipantInfo, callbackUrl string, clientFactory mlnodeclient.ClientFactory, configManager *apiconfig.ConfigManager) *Broker {
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
		statusQueryTrigger:   make(chan statusQuerySignal, 1),
		configManager:        configManager,
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

type statusQuerySignal struct {
	BypassDebounce bool
}

func (b *Broker) TriggerStatusQuery(bypassDebounce bool) {
	select {
	case b.statusQueryTrigger <- statusQuerySignal{BypassDebounce: bypassDebounce}:
	default: // Non-blocking send
	}
}

func (b *Broker) GetChainBridge() BrokerChainBridge {
	return b.chainBridge
}

func (b *Broker) LoadNodeToBroker(node *apiconfig.InferenceNodeConfig) chan NodeCommandResponse {
	if node == nil {
		return nil
	}

	cmd := NewRegisterNodeCommand(*node)
	err := b.QueueMessage(cmd)
	if err != nil {
		logging.Error("Error loading node to broker", types.Nodes, "error", err)
		panic(err)
		// return nil
	}
	return cmd.Response
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
		command.Execute(b)
	case RemoveNode:
		command.Execute(b)
	case UpdateNode:
		command.Execute(b)
	case GetNodesCommand:
		command.Execute(b)
	case SyncNodesCommand:
		b.syncNodes()
	case LockNodesForTrainingCommand:
		b.lockNodesForTraining(command)
	case StartTrainingCommand:
		command.Execute(b)
	case SetNodesActualStatusCommand:
		command.Execute(b)
	case SetNodeAdminStateCommand:
		command.Execute(b)
	case UpdateNodeHardwareCommand:
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

func (b *Broker) NewNodeClient(node *Node) mlnodeclient.MLNodeClient {
	version := b.configManager.GetCurrentNodeVersion()
	return b.mlNodeClientFactory.CreateClient(node.PoCUrlWithVersion(version), node.InferenceUrlWithVersion(version))
}

func (b *Broker) lockAvailableNode(command LockAvailableNode) {
	leastBusyNode := b.getLeastBusyNode(command)

	if leastBusyNode != nil {
		b.mu.RLock()
		leastBusyNode.State.LockCount++
		b.mu.RUnlock()
	}
	logging.Debug("Locked node", types.Nodes, "node", leastBusyNode)
	if leastBusyNode == nil {
		command.Response <- nil
	} else {
		command.Response <- &leastBusyNode.Node
	}
}

func (b *Broker) getLeastBusyNode(command LockAvailableNode) *NodeWithState {
	epochState := b.phaseTracker.GetCurrentEpochState()
	if epochState.IsNilOrNotSynced() {
		logging.Error("getLeastBusyNode. Cannot get least busy node, epoch state is empty", types.Nodes)
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Build skip set
	skip := make(map[string]struct{}, len(command.SkipNodeIDs))
	for _, id := range command.SkipNodeIDs {
		if id != "" {
			skip[id] = struct{}{}
		}
	}

	var leastBusyNode *NodeWithState = nil
	for _, node := range b.nodes {
		if _, shouldSkip := skip[node.Node.Id]; shouldSkip {
			logging.Info("Node skipped by LockAvailableNode skip list", types.Nodes, "node_id", node.Node.Id)
			continue
		}
		// TODO: log some kind of a reason as to why the node is not available
		if available, reason := b.nodeAvailable(node, command.Model, epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase); available {
			if leastBusyNode == nil || node.State.LockCount < leastBusyNode.State.LockCount {
				leastBusyNode = node
			}
		} else {
			logging.Info("Node not available", types.Nodes, "node_id", node.Node.Id, "reason", reason)
		}
	}

	return leastBusyNode
}

type NodeNotAvailableReason = string

func (b *Broker) nodeAvailable(node *NodeWithState, neededModel string, currentEpoch uint64, currentPhase types.EpochPhase) (bool, NodeNotAvailableReason) {
	if node.State.IntendedStatus != types.HardwareNodeStatus_INFERENCE {
		return false, fmt.Sprintf("Node is not intended for INFERENCE at the moment: %s", node.State.IntendedStatus)
	}
	logging.Info("nodeAvailable. Node is intended for INFERENCE", types.Nodes, "nodeId", node.Node.Id, "intendedStatus", node.State.IntendedStatus)

	if node.State.CurrentStatus != types.HardwareNodeStatus_INFERENCE {
		return false, fmt.Sprintf("Node is not in INFERENCE state: %s", node.State.CurrentStatus)
	}
	logging.Info("nodeAvailable. Node is in INFERENCE state", types.Nodes, "nodeId", node.Node.Id)

	if node.State.ReconcileInfo != nil {
		return false, fmt.Sprintf("Node is currently reconciling: %s", node.State.ReconcileInfo.Status)
	}
	logging.Info("nodeAvailable. Node is not being reconciled, ReconcileInfo == nil", types.Nodes, "nodeId", node.Node.Id)

	if node.State.LockCount >= node.Node.MaxConcurrent {
		return false, fmt.Sprintf("Node is locked too many times: lockCount=%d, maxConcurrent=%d", node.State.LockCount, node.Node.MaxConcurrent)
	}
	logging.Info("nodeAvailable. Node is not locked too many times", types.Nodes, "nodeId", node.Node.Id, "lockCount", node.State.LockCount, "maxConcurrent", node.Node.MaxConcurrent)

	// Check admin state using provided epoch and phase
	if !node.State.ShouldBeOperational(currentEpoch, currentPhase) {
		return false, fmt.Sprintf("Node is administratively disabled: currentEpoch=%v, currentPhase=%s, adminState = %v", currentEpoch, currentPhase, node.State.AdminState)
	}
	logging.Info("nodeAvailable. Node is not administratively enabled", types.Nodes, "nodeId", node.Node.Id, "adminState", node.State.AdminState)

	_, found := node.State.EpochModels[neededModel]
	if !found {
		logging.Info("Node does not have neededModel", types.Nodes, "node_id", node.Node.Id, "neededModel", neededModel)
		return false, fmt.Sprintf("Node does not have model %s", neededModel)
	} else {
		logging.Info("Node has neededModel", types.Nodes, "node_id", node.Node.Id, "neededModel", neededModel)
		return true, ""
	}
}

func (b *Broker) releaseNode(command ReleaseNode) {
	b.mu.RLock()
	node, ok := b.nodes[command.NodeId]
	b.mu.RUnlock()

	if !ok {
		command.Response <- false
		return
	} else {
		b.mu.RLock()
		node.State.LockCount--
		b.mu.RUnlock()
		if !command.Outcome.IsSuccess() {
			logging.Error("Node failed", types.Nodes, "node_id", command.NodeId, "reason", command.Outcome.GetMessage())
			// FIXME: need a write lock here?
			//  not sure if we should update the state, we have health checks for that
			// node.State.Failure("Inference failed")
		}
	}
	logging.Debug("Released node", types.Nodes, "node_id", command.NodeId)
	command.Response <- true
}

var ErrNoNodesAvailable = errors.New("no nodes available for inference")

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
		return zero, ErrNoNodesAvailable
	}

	defer func() {
		queueError := b.QueueMessage(ReleaseNode{
			NodeId:   node.Id,
			Outcome:  InferenceSuccess{},
			Response: make(chan bool, 2),
		})

		if queueError != nil {
			logging.Error("Error releasing node", types.Nodes, "error", queueError)
		}
	}()

	return action(node)
}

// FIXME: Should return a copy! To avoid modifying state outside of the broker
func (b *Broker) GetNodes() ([]NodeResponse, error) {
	command := NewGetNodesCommand()
	err := b.QueueMessage(command)
	if err != nil {
		return nil, err
	}
	nodes := <-command.Response

	if nodes == nil {
		return nil, errors.New("Error getting nodes")
	}
	logging.Debug("Got nodes", types.Nodes, "size", len(nodes))
	return nodes, nil
}

func (b *Broker) GetNodeByNodeNum(nodeNum uint64) (*Node, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, nodeWithState := range b.nodes {
		if nodeWithState.Node.NodeNum == nodeNum {
			return &nodeWithState.Node, true
		}
	}

	return nil, false
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

	if len(diff.Removed) == 0 && len(diff.NewOrModified) == 0 {
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

// Timeouts for node health checks used in queryNodeStatus
const (
	nodeStatusRequestTimeout      = 5 * time.Second
	inferenceHealthRequestTimeout = 5 * time.Second
	// statusScanMinInterval enforces a minimal delay between consecutive full scans
	// in nodeStatusQueryWorker, covering both manual and timer triggers.
	statusScanMinInterval = 2 * time.Second
)

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
			b.reconcileIfSynced("Reconciliation triggered manually")
		case <-ticker.C:
			b.reconcileIfSynced("Reconciliation triggered by timer")
			// Check for version changes and refresh clients if needed
			b.checkAndRefreshClientsIfNeeded()
		}
	}
}

type VersionHealthReport struct {
	IsAlive bool   `json:"is_alive"`
	Error   string `json:"error,omitempty"`
}

func (b *Broker) CheckVersionHealth(version string) map[string]VersionHealthReport {
	b.mu.RLock()
	nodeIds := make([]string, 0, len(b.nodes))
	for nodeId := range b.nodes {
		nodeIds = append(nodeIds, nodeId)
	}
	b.mu.RUnlock()

	reports := make(map[string]VersionHealthReport)
	var wg sync.WaitGroup
	var reportsMu sync.Mutex

	for _, nodeId := range nodeIds {
		wg.Add(1)
		go func(nodeId string) {
			defer wg.Done()
			worker, exists := b.nodeWorkGroup.GetWorker(nodeId)
			report := VersionHealthReport{}

			if !exists {
				report.Error = "worker not found"
			} else {
				alive, err := worker.CheckClientVersionAlive(version, b.mlNodeClientFactory)
				report.IsAlive = alive
				if err != nil {
					report.Error = err.Error()
				}
			}

			reportsMu.Lock()
			reports[nodeId] = report
			reportsMu.Unlock()
		}(nodeId)
	}

	wg.Wait()
	return reports
}

// checkAndRefreshClientsIfNeeded checks if the MLNode version has changed and refreshes all clients if needed
func (b *Broker) checkAndRefreshClientsIfNeeded() {
	if b.configManager.ShouldRefreshClients() {
		currentVersion := b.configManager.GetCurrentNodeVersion()
		lastUsedVersion := b.configManager.GetLastUsedVersion()

		logging.Info("MLNode version change detected - immediately refreshing all clients", types.Nodes,
			"oldVersion", lastUsedVersion, "newVersion", currentVersion)

		// Immediately refresh all worker clients (no queuing delay)
		b.mu.RLock()
		workerIds := make([]string, 0, len(b.nodes))
		for nodeId := range b.nodes {
			workerIds = append(workerIds, nodeId)
		}
		b.mu.RUnlock()

		// Immediately refresh all workers
		refreshedCount := 0
		for _, nodeId := range workerIds {
			worker, exists := b.nodeWorkGroup.GetWorker(nodeId)
			if exists {
				worker.RefreshClientImmediate(lastUsedVersion, currentVersion)
				refreshedCount++
			}
		}

		logging.Info("Immediately refreshed all MLNode clients", types.Nodes,
			"oldVersion", lastUsedVersion, "newVersion", currentVersion, "count", refreshedCount)

		// Update last used version (fire and forget - if this fails, we'll retry next cycle)
		if err := b.configManager.SetLastUsedVersion(currentVersion); err != nil {
			logging.Warn("Failed to update last used version", types.Config, "error", err)
		}
	} else {
		// Ensure lastUsedVersion is set if it's empty (first time initialization)
		if b.configManager.GetLastUsedVersion() == "" {
			currentVersion := b.configManager.GetCurrentNodeVersion()
			if currentVersion != "" {
				if err := b.configManager.SetLastUsedVersion(currentVersion); err != nil {
					logging.Warn("Failed to initialize last used version", types.Config, "error", err)
				}
			}
		}
	}
	upgradeVersion := b.configManager.GetUpgradePlan().NodeVersion
	if upgradeVersion != "" {
		reports := b.CheckVersionHealth(upgradeVersion)
		for nodeId, report := range reports {
			if report.Error != "" {
				logging.Warn("Failed to check MLNode version in upgrade plan", types.Nodes, "node_id", nodeId, "error", report.Error)
			} else if !report.IsAlive {
				logging.Warn("MLNode version in upgrade plan is not alive", types.Nodes, "node_id", nodeId)
			} else {
				logging.Debug("MLNode version in upgrade plan is alive", types.Nodes, "node_id", nodeId)
			}
		}
	}
}

func (b *Broker) reconcileIfSynced(triggerMsg string) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochState()
	if epochPhaseInfo.IsNilOrNotSynced() {
		logging.Warn("Reconciliation triggered while epoch phase info is not synced. Skipping", types.Nodes)
		return
	}

	logging.Info(triggerMsg, types.Nodes, "blockHeight", epochPhaseInfo.CurrentBlock.Height)
	b.reconcile(*epochPhaseInfo)
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

		// TODO: we should make reindexing as some indexes might be skipped
		totalNumNodes := b.curMaxNodesNum.Load() + 1
		// Create and dispatch the command
		cmd := b.getCommandForState(&node.State, currentPoCParams, pocParamsErr, int(totalNumNodes))
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
		// CONFIRMATION PoC - use hash from event (populated by chain at generation_start_height)
		if epochState.CurrentPhase == types.InferencePhase && epochState.ActiveConfirmationPoCEvent != nil {
			event := epochState.ActiveConfirmationPoCEvent
			return &pocParams{
				startPoCBlockHeight: event.TriggerHeight,
				startPoCBlockHash:   event.PocSeedBlockHash,
			}, nil
		}

		// REGULAR PoC - query hash as usual
		currentPoCParams, pocParamsErr := b.queryCurrentPoCParams(epochState.LatestEpoch.PocStartBlockHeight)
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
	case types.HardwareNodeStatus_TRAINING:
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

func nodeStatusQueryWorker(broker *Broker) {
	checkInterval := 60 * time.Second
	// Track when an actual scan starts to enforce a minimal interval between scans
	var lastScanStart time.Time
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		bypassDebounce := false
		select {
		case <-ticker.C:
			logging.Debug("nodeStatusQueryWorker triggered by ticker", types.Nodes)
		case sig := <-broker.statusQueryTrigger:
			logging.Debug("nodeStatusQueryWorker triggered manually", types.Nodes)
			bypassDebounce = sig.BypassDebounce
		}

		// Enforce minimal interval between scans unless bypass requested
		if !bypassDebounce && !lastScanStart.IsZero() {
			elapsed := time.Since(lastScanStart)
			if elapsed < statusScanMinInterval {
				logging.Debug("nodeStatusQueryWorker skipping scan due to min interval", types.Nodes, "elapsed", elapsed)
				continue
			}
		}
		// Mark the start of an actual scan
		lastScanStart = time.Now()

		nodes, err := broker.GetNodes()
		if err != nil {
			logging.Error("nodeStatusQueryWorker. Failed to get nodes for status query", types.Nodes, "error", err)
			continue
		}

		statusUpdates := make([]StatusUpdate, 0)

		for _, nodeResp := range nodes {
			// Only check nodes that are UNKNOWN or haven't been checked in a while.
			sinceLastStatusCheck := time.Since(nodeResp.State.StatusTimestamp)
			if nodeResp.State.CurrentStatus != types.HardwareNodeStatus_UNKNOWN && sinceLastStatusCheck < checkInterval {
				logging.Info("nodeStatusQueryWorker skipping status query for node", types.Nodes,
					"nodeId", nodeResp.Node.Id,
					"currentStatus", nodeResp.State.CurrentStatus.String(),
					"sinceLastStatusCheck", sinceLastStatusCheck)
				continue
			}

			queryStatusResult, err := broker.queryNodeStatus(nodeResp.Node, nodeResp.State)
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

		if len(statusUpdates) > 0 {
			err = broker.QueueMessage(NewSetNodesActualStatusCommand(statusUpdates))
			logging.Info("nodeStatusQueryWorker. Queued status updates submitted", types.Nodes,
				"len(statusUpdates)", len(statusUpdates))
			if err != nil {
				logging.Error("nodeStatusQueryWorker. Failed to queue status update command", types.Nodes, "error", err)
				continue
			}
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

	ctx, cancel := context.WithTimeout(context.Background(), nodeStatusRequestTimeout)
	defer cancel()
	status, err := client.NodeState(ctx)

	nodeId := node.Id
	prevStatus := state.CurrentStatus
	var currentStatus types.HardwareNodeStatus
	if err != nil {
		logging.Error("queryNodeStatus. Failed to query node status. Assuming currentStatus = FAILED", types.Nodes,
			"nodeId", nodeId, "error", err)
		currentStatus = types.HardwareNodeStatus_FAILED
	} else {
		currentStatus = toStatus(*status)
	}

	logging.Info("queryNodeStatus. Queried node status", types.Nodes, "nodeId", nodeId, "currentStatus", currentStatus.String(), "prevStatus", prevStatus.String())

	if currentStatus == types.HardwareNodeStatus_INFERENCE {
		hctx, hcancel := context.WithTimeout(context.Background(), inferenceHealthRequestTimeout)
		defer hcancel()
		ok, err := client.InferenceHealth(hctx)
		if !ok || err != nil {
			currentStatus = types.HardwareNodeStatus_FAILED
			logging.Info("queryNodeStatus. Node inference health check failed", types.Nodes, "nodeId", nodeId, "currentStatus", currentStatus.String(), "prevStatus", prevStatus.String(), "err", err)
		}
	}

	// TODO: probably should also check PoC sub status here
	//  but before implementing it, need to check we treat them correctly during reconciliation
	//  for example I think we expect IDLE instead of STOPPED for PoC nodes
	//  which is actually wrong

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

// UpdateNodeWithEpochData queries the current epoch group data from the chain
// and populates the NodeState with the epoch-specific model and MLNode info.
// It only performs the update if the epoch index or phase has changed.
func (b *Broker) UpdateNodeWithEpochData(epochState *chainphase.EpochState) error {
	if epochState.LatestEpoch.EpochIndex <= b.lastEpochIndex && epochState.CurrentPhase == b.lastEpochPhase {
		return nil // No change, no need to update
	}

	// Update the broker's last known state
	b.lastEpochIndex = epochState.LatestEpoch.EpochIndex
	b.lastEpochPhase = epochState.CurrentPhase

	logging.Info("Epoch or phase change detected, updating node data with epoch info.", types.Nodes,
		"old_epoch", b.lastEpochIndex, "new_epoch", epochState.LatestEpoch.EpochIndex,
		"old_phase", b.lastEpochPhase, "new_phase", epochState.CurrentPhase)

	// 1. Get the parent epoch group to find all subgroup models
	parentGroupResp, err := b.chainBridge.GetEpochGroupDataByModelId(epochState.LatestEpoch.EpochIndex, "")
	if err != nil {
		logging.Error("Failed to get parent epoch group", types.Nodes, "error", err)
		return err
	}
	if parentGroupResp == nil {
		logging.Error("Parent epoch group data is nil", types.Nodes, "epoch_index", epochState.LatestEpoch.EpochIndex, "epoch_poc_start_block_height", epochState.LatestEpoch.PocStartBlockHeight, "epoch_group_data_poc_start_block_height")
		return nil
	}
	if len(parentGroupResp.EpochGroupData.SubGroupModels) == 0 {
		logging.Warn("Parent epoch group SubGroupModels are empty", types.Nodes, "epoch_index", epochState.LatestEpoch.EpochIndex, "epoch_poc_start_block_height", epochState.LatestEpoch.PocStartBlockHeight, "epoch_group_data_poc_start_block_height", parentGroupResp.EpochGroupData.PocStartBlockHeight)
		return nil
	}

	parentEpochData := parentGroupResp.GetEpochGroupData()

	b.clearNodeEpochData()

	// 2. Iterate through each model subgroup
	for _, modelId := range parentEpochData.SubGroupModels {
		subgroupResp, err := b.chainBridge.GetEpochGroupDataByModelId(parentEpochData.EpochIndex, modelId)
		if err != nil {
			logging.Error("Failed to get subgroup epoch data", types.Nodes, "model_id", modelId, "error", err)
			continue
		}
		if subgroupResp == nil {
			logging.Warn("Subgroup epoch data response is nil", types.Nodes, "model_id", modelId)
			continue
		}

		subgroup := subgroupResp.EpochGroupData
		if subgroup.ModelSnapshot == nil {
			logging.Error("ModelSnapshot is nil in subgroup", types.Nodes, "model_id", modelId)
			continue
		}

		// 3. Iterate through participants in the subgroup
		for _, weightInfo := range subgroup.ValidationWeights {
			// Check if the participant is the one this broker is managing
			if weightInfo.MemberAddress == b.participantInfo.GetAddress() {
				// 4. Iterate through the ML nodes for this participant in the epoch data
				b.UpdateNodeEpochData(weightInfo.MlNodes, modelId, *subgroup.ModelSnapshot)
			}
		}
	}

	return nil
}

func (b *Broker) clearNodeEpochData() {
	b.mu.Lock()
	defer b.mu.Unlock()

	logging.Info("Clearing node epoch data", types.Nodes)
	for _, node := range b.nodes {
		node.State.EpochModels = make(map[string]types.Model)
		node.State.EpochMLNodes = make(map[string]types.MLNodeInfo)
	}
}

func (b *Broker) UpdateNodeEpochData(mlNodes []*types.MLNodeInfo, modelId string, modelSnapshot types.Model) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, mlNodeInfo := range mlNodes {
		if node, ok := b.nodes[mlNodeInfo.NodeId]; ok {
			node.State.EpochModels[modelId] = modelSnapshot
			node.State.EpochMLNodes[modelId] = *mlNodeInfo
			logging.Info("Updated epoch data for node", types.Nodes, "node_id", node.Node.Id, "model_id", modelId)
		}
	}
}

// MergeModelArgs combines model arguments from the epoch snapshot with locally
// configured arguments, with epoch arguments taking precedence.
// It understands arguments as --key or --key value pairs.
func (b *Broker) MergeModelArgs(epochArgs []string, localArgs []string) []string {
	// The final merged arguments, preserving the order from epochArgs, then localArgs.
	mergedArgs := make([]string, 0, len(epochArgs)+len(localArgs))
	// A set to store the keys from epochArgs to check for precedence.
	epochKeys := make(map[string]struct{})

	// 1. Process epochArgs first. They all go into the result and populate epochKeys.
	for i := 0; i < len(epochArgs); i++ {
		arg := epochArgs[i]
		if strings.HasPrefix(arg, "--") {
			key := arg
			epochKeys[key] = struct{}{}
			mergedArgs = append(mergedArgs, key)

			// Check if the next element is a value for this key.
			if i+1 < len(epochArgs) && !strings.HasPrefix(epochArgs[i+1], "--") {
				// It's a value, add it to mergedArgs and skip it in the next iteration.
				mergedArgs = append(mergedArgs, epochArgs[i+1])
				i++
			}
		} else {
			// This case handles a value without a preceding key in epochArgs,
			// which is unlikely but we add it to be safe.
			mergedArgs = append(mergedArgs, arg)
		}
	}

	// 2. Process localArgs and add only the ones with keys not present in epochArgs.
	for i := 0; i < len(localArgs); i++ {
		arg := localArgs[i]
		if strings.HasPrefix(arg, "--") {
			key := arg
			if _, exists := epochKeys[key]; !exists {
				// This key is not in epochArgs, so we can add it.
				mergedArgs = append(mergedArgs, key)

				// Check if it has a value.
				if i+1 < len(localArgs) && !strings.HasPrefix(localArgs[i+1], "--") {
					// It has a value, add it and skip.
					mergedArgs = append(mergedArgs, localArgs[i+1])
					i++
				}
			} else {
				// Key already exists in epoch args, so we skip it.
				// If it has a value, we need to skip that too.
				if i+1 < len(localArgs) && !strings.HasPrefix(localArgs[i+1], "--") {
					i++ // Skip the value of the overridden key.
				}
			}
		}
		// Non-key arguments are ignored here as they are considered values
		// of keys, which are handled within the loop.
	}

	return mergedArgs
}
