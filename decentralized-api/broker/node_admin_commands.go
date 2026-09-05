package broker

import (
	"common/logging"
	"context"
	"decentralized-api/apiconfig"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

var ErrNodeAlreadyExists = errors.New("node already exists")

// validateInferenceNode validates an InferenceNodeConfig and returns an error if invalid.
// The error message describes what is wrong with the node configuration.
// excludeNodeId is used when updating a node - it excludes that node from duplicate checks.
// This method is exported so it can be called from admin handlers to provide clear error messages.
func (b *Broker) validateInferenceNode(node apiconfig.InferenceNodeConfig, excludeNodeId string) error {
	errors := apiconfig.ValidateInferenceNodeBasic(node)

	b.mu.RLock()
	dupes := b.duplicateEndpointErrorsLocked(node, excludeNodeId)
	b.mu.RUnlock()
	errors = append(errors, dupes...)

	if len(errors) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// duplicateEndpointErrorsLocked reports host+port collisions. Caller must hold b.mu.
func (b *Broker) duplicateEndpointErrorsLocked(node apiconfig.InferenceNodeConfig, excludeNodeId string) []string {
	var errors []string
	for id, existingNode := range b.nodes {
		if excludeNodeId != "" && id == excludeNodeId {
			continue
		}
		if existingNode.Node.Host == node.Host && existingNode.Node.InferencePort == node.InferencePort {
			errors = append(errors, fmt.Sprintf("duplicate inference host+port combination: %s:%d (already used by node '%s')", node.Host, node.InferencePort, id))
			break
		}
	}
	for id, existingNode := range b.nodes {
		if excludeNodeId != "" && id == excludeNodeId {
			continue
		}
		if existingNode.Node.Host == node.Host && existingNode.Node.PoCPort == node.PoCPort {
			errors = append(errors, fmt.Sprintf("duplicate PoC host+port combination: %s:%d (already used by node '%s')", node.Host, node.PoCPort, id))
			break
		}
	}
	return errors
}

type RegisterNode struct {
	Node     apiconfig.InferenceNodeConfig
	Response chan NodeCommandResponse
}

func NewRegisterNodeCommand(node apiconfig.InferenceNodeConfig) RegisterNode {
	return RegisterNode{
		Node:     node,
		Response: make(chan NodeCommandResponse, 2),
	}
}

func (r RegisterNode) GetResponseChannelCapacity() int {
	return cap(r.Response)
}

func (c RegisterNode) Execute(b *Broker) {
	// Exclude this id so a retry of an existing node fails as already-exists,
	// not as a host+port collision against itself.
	if err := b.validateInferenceNode(c.Node, c.Node.Id); err != nil {
		logging.Error("RegisterNode. Node validation failed", types.Nodes, "node_id", c.Node.Id, "error", err)
		c.Response <- NodeCommandResponse{Node: nil, Error: err}
		return
	}

	govModels, err := b.chainBridge.GetGovernanceModels()
	if err != nil {
		logging.Error("RegisterNode. Failed to get governance models", types.Nodes, "error", err)
		c.Response <- NodeCommandResponse{Node: nil, Error: err}
		return
	}

	modelMap := make(map[string]struct{})
	for _, model := range govModels.Model {
		logging.Info("RegisterNode. Governance model", types.Nodes, "model_id", model.Id)
		modelMap[model.Id] = struct{}{}
	}

	for modelId := range c.Node.Models {
		if _, ok := modelMap[modelId]; !ok {
			logging.Warn("RegisterNode. Dropping non-governance model", types.Nodes, "node_id", c.Node.Id, "model_id", modelId)
			delete(c.Node.Models, modelId)
		}
	}
	if len(c.Node.Models) == 0 {
		err := fmt.Errorf("node %s has no governance-valid models", c.Node.Id)
		logging.Error("RegisterNode. No valid models after filter", types.Nodes, "node_id", c.Node.Id)
		c.Response <- NodeCommandResponse{Node: nil, Error: err}
		return
	}

	models := make(map[string]ModelArgs)
	for model, config := range c.Node.Models {
		models[model] = modelArgsFromConfig(config)
	}

	var currentEpoch uint64
	if b.phaseTracker != nil {
		epochState := b.phaseTracker.GetCurrentEpochState()
		if epochState == nil {
			currentEpoch = 0
		} else {
			currentEpoch = epochState.LatestEpoch.EpochIndex
		}
	}

	b.mu.Lock()
	if _, exists := b.nodes[c.Node.Id]; exists {
		b.mu.Unlock()
		logging.Error("RegisterNode. Node already exists", types.Nodes, "node_id", c.Node.Id)
		c.Response <- NodeCommandResponse{Node: nil, Error: fmt.Errorf("%w: %s", ErrNodeAlreadyExists, c.Node.Id)}
		return
	}
	if _, exists := b.nodeWorkGroup.GetWorker(c.Node.Id); exists {
		b.mu.Unlock()
		logging.Error("RegisterNode. Worker already exists for id with no node entry", types.Nodes, "node_id", c.Node.Id)
		c.Response <- NodeCommandResponse{Node: nil, Error: fmt.Errorf("%w: %s", ErrNodeAlreadyExists, c.Node.Id)}
		return
	}
	if dupes := b.duplicateEndpointErrorsLocked(c.Node, ""); len(dupes) > 0 {
		b.mu.Unlock()
		err := fmt.Errorf("validation failed: %s", strings.Join(dupes, "; "))
		logging.Error("RegisterNode. Node validation failed", types.Nodes, "node_id", c.Node.Id, "error", err)
		c.Response <- NodeCommandResponse{Node: nil, Error: err}
		return
	}

	nodeNum := b.curMaxNodesNum.Add(1)
	registrationSeq := b.nextRegistrationSeq.Add(1)
	nodeWithState := &NodeWithState{
		Node: Node{
			Host:             c.Node.Host,
			InferenceSegment: c.Node.InferenceSegment,
			InferencePort:    c.Node.InferencePort,
			PoCSegment:       c.Node.PoCSegment,
			PoCPort:          c.Node.PoCPort,
			Models:           models,
			Id:               c.Node.Id,
			MaxConcurrent:    c.Node.MaxConcurrent,
			NodeNum:          nodeNum,
			Hardware:         c.Node.Hardware,
		},
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
			EpochModels:     make(map[string]types.Model),
			EpochMLNodes:    make(map[string]types.MLNodeInfo),
			RegistrationSeq: registrationSeq,
		},
	}
	worker := NewNodeWorker(c.Node.Id, nodeWithState, b)
	if !b.nodeWorkGroup.AddWorker(c.Node.Id, worker) {
		worker.signalShutdown()
		b.mu.Unlock()
		logging.Error("RegisterNode. Worker already exists for id with no node entry", types.Nodes, "node_id", c.Node.Id)
		c.Response <- NodeCommandResponse{Node: nil, Error: fmt.Errorf("%w: %s", ErrNodeAlreadyExists, c.Node.Id)}
		return
	}
	b.nodes[c.Node.Id] = nodeWithState
	b.mu.Unlock()

	// Populate epoch data for the newly registered node
	if err := b.PopulateSingleNodeEpochData(c.Node.Id); err != nil {
		logging.Warn("RegisterNode. Failed to populate epoch data", types.Nodes, "node_id", c.Node.Id, "error", err)
	}
	b.refreshDeploymentUpdatePendingFromApplied(c.Node.Id)

	// Trigger a status check for the newly added node.
	b.TriggerStatusQuery(true)
	b.TriggerReconciliation()

	logging.Info("RegisterNode. Registered node", types.Nodes, "node", c.Node)
	c.Response <- NodeCommandResponse{Node: &c.Node, Error: nil}
}

// UpdateNode updates an existing node's configuration while preserving runtime state
type UpdateNode struct {
	Node     apiconfig.InferenceNodeConfig
	Response chan NodeCommandResponse
}

type NodeCommandResponse struct {
	Node  *apiconfig.InferenceNodeConfig
	Error error
}

func NewUpdateNodeCommand(node apiconfig.InferenceNodeConfig) UpdateNode {
	return UpdateNode{
		Node:     node,
		Response: make(chan NodeCommandResponse, 2),
	}
}

func (u UpdateNode) GetResponseChannelCapacity() int {
	return cap(u.Response)
}

func (c UpdateNode) Execute(b *Broker) {
	// Fetch existing node first to check if it exists
	b.mu.RLock()
	existing, exists := b.nodes[c.Node.Id]
	b.mu.RUnlock()

	if !exists {
		logging.Error("UpdateNode. Node not found", types.Nodes, "node_id", c.Node.Id)
		c.Response <- NodeCommandResponse{Node: nil, Error: fmt.Errorf("node not found: %s", c.Node.Id)}
		return
	}

	// Validate node configuration (exclude current node from duplicate checks)
	if err := b.validateInferenceNode(c.Node, c.Node.Id); err != nil {
		logging.Error("UpdateNode. Node validation failed", types.Nodes, "node_id", c.Node.Id, "error", err)
		c.Response <- NodeCommandResponse{Node: nil, Error: err}
		return
	}

	// Validate models exist in governance
	govModels, err := b.chainBridge.GetGovernanceModels()
	if err != nil {
		logging.Error("UpdateNode. Failed to get governance models", types.Nodes, "error", err)
		c.Response <- NodeCommandResponse{Node: nil, Error: err}
		return
	}

	modelMap := make(map[string]struct{})
	for _, model := range govModels.Model {
		modelMap[model.Id] = struct{}{}
	}

	for modelId := range c.Node.Models {
		if _, ok := modelMap[modelId]; !ok {
			logging.Error("UpdateNode. Model is not a valid governance model", types.Nodes, "model_id", modelId)
			c.Response <- NodeCommandResponse{Node: nil, Error: fmt.Errorf("model %s is not a valid governance model", modelId)}
			return
		}
	}

	// Apply update
	b.mu.Lock()
	defer b.mu.Unlock()

	// Build updated Node struct, preserving node number
	models := make(map[string]ModelArgs)
	for model, config := range c.Node.Models {
		models[model] = modelArgsFromConfig(config)
	}
	deploymentChanged, assignedModelRemoved := activeDeploymentChanged(existing.State.EpochMLNodes, existing.Node.Models, models)

	updated := Node{
		Host:             c.Node.Host,
		InferenceSegment: c.Node.InferenceSegment,
		InferencePort:    c.Node.InferencePort,
		PoCSegment:       c.Node.PoCSegment,
		PoCPort:          c.Node.PoCPort,
		Models:           models,
		Id:               c.Node.Id,
		MaxConcurrent:    c.Node.MaxConcurrent,
		NodeNum:          existing.Node.NodeNum,
		Hardware:         c.Node.Hardware,
	}

	// Apply update
	existing.Node = updated
	if deploymentChanged {
		if shouldCancelForDeploymentChange(existing.State) {
			existing.State.cancelInFlightTask()
			existing.State.cancelInFlightTask = nil
			existing.State.ReconcileInfo = nil
		}
		existing.State.DeploymentUpdatePending = true
		existing.State.DeploymentRetryAfter = time.Time{}
	}

	// Optionally trigger a status re-check
	b.TriggerStatusQuery(true)
	if deploymentChanged {
		b.TriggerReconciliation()
	}
	if assignedModelRemoved {
		logging.Info("UpdateNode. Assigned model is no longer supported; waiting for chain assignment", types.Nodes,
			"node_id", c.Node.Id)
	}

	logging.Info("UpdateNode. Updated node configuration", types.Nodes, "node_id", c.Node.Id)
	c.Response <- NodeCommandResponse{Node: &c.Node, Error: nil}
}

func shouldCancelForDeploymentChange(state NodeState) bool {
	return state.cancelInFlightTask != nil &&
		state.ReconcileInfo != nil &&
		state.ReconcileInfo.Status == types.HardwareNodeStatus_INFERENCE
}

type RemoveNode struct {
	NodeId   string
	Response chan bool
}

const appliedDeploymentDeleteTimeout = time.Second

func (r RemoveNode) GetResponseChannelCapacity() int {
	return cap(r.Response)
}

func (command RemoveNode) Execute(b *Broker) {
	// Unregister immediately and never wait on processCommands. Worker HTTP
	// (inference/up, stop, PoC) can take up to the ML-node client timeout
	// (15 minutes). Waiting here stalls StartPocCommand for every node.
	var cancel func()
	existed := false

	b.mu.Lock()
	if node, ok := b.nodes[command.NodeId]; ok {
		existed = true
		cancel = node.State.cancelInFlightTask
		node.State.cancelInFlightTask = nil
		node.State.ReconcileInfo = nil
		delete(b.nodes, command.NodeId)
	}
	b.mu.Unlock()

	// Stop the worker before cancelling in-flight HTTP. Cancel can make
	// Execute return immediately; if the run loop is not stopping yet it
	// will start the next queued command (another 15-minute call).
	b.nodeWorkGroup.RemoveWorker(command.NodeId)
	if cancel != nil {
		cancel()
	}

	if !existed {
		command.Response <- false
		return
	}
	if b.configManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), appliedDeploymentDeleteTimeout)
		err := b.configManager.DeleteAppliedDeploymentsForNode(ctx, command.NodeId)
		cancel()
		if err != nil {
			logging.Warn("Failed to delete applied deployments for removed node", types.Config,
				"node_id", command.NodeId, "error", err)
			b.noteStaleApplied(command.NodeId)
		}
	}
	logging.Debug("Removed node", types.Nodes, "node_id", command.NodeId)
	command.Response <- true
}

// SetNodeAdminStateCommand enables or disables a node administratively
type SetNodeAdminStateCommand struct {
	NodeId   string
	Enabled  bool
	Response chan error
}

func (c SetNodeAdminStateCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c SetNodeAdminStateCommand) Execute(b *Broker) {
	// Get current epoch
	var currentEpoch uint64
	if b.phaseTracker != nil {
		epochState := b.phaseTracker.GetCurrentEpochState()
		if epochState == nil {
			currentEpoch = 0
		} else {
			currentEpoch = epochState.LatestEpoch.EpochIndex
		}
	}

	err := c.modifyNodeAdminState(b, currentEpoch)
	if err != nil {
		logging.Error("Failed to set node admin state", types.Nodes, "node_id", c.NodeId, "error", err)
		c.Response <- err
	} else {
		logging.Info("Updated node admin state", types.Nodes,
			"node_id", c.NodeId,
			"enabled", c.Enabled,
			"epoch", currentEpoch)
		c.Response <- nil
	}
}

func (c SetNodeAdminStateCommand) modifyNodeAdminState(b *Broker, currentEpoch uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	node, exists := b.nodes[c.NodeId]
	if !exists {
		return fmt.Errorf("node not found: %s", c.NodeId)
	}

	// Update admin state
	node.State.AdminState.Enabled = c.Enabled
	node.State.AdminState.Epoch = currentEpoch

	return nil
}

// UpdateNodeHardwareCommand updates the Hardware field for a specific node
type UpdateNodeHardwareCommand struct {
	NodeId   string
	Hardware []apiconfig.Hardware
	Response chan error
}

func (c UpdateNodeHardwareCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c UpdateNodeHardwareCommand) Execute(b *Broker) {
	b.mu.Lock()
	defer b.mu.Unlock()

	node, exists := b.nodes[c.NodeId]
	if !exists {
		c.Response <- fmt.Errorf("node not found: %s", c.NodeId)
		return
	}

	node.Node.Hardware = c.Hardware
	logging.Info("Updated node hardware", types.Nodes, "node_id", c.NodeId, "hardware_count", len(c.Hardware))
	c.Response <- nil
}
