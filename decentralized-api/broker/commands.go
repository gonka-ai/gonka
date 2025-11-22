package broker

import (
	"decentralized-api/apiconfig"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type Command interface {
	GetResponseChannelCapacity() int
}

type LockAvailableNode struct {
	Model       string
	Response    chan *Node
	SkipNodeIDs []string
}

func (g LockAvailableNode) GetResponseChannelCapacity() int {
	return cap(g.Response)
}

type ReleaseNode struct {
	NodeId   string
	Outcome  InferenceResult
	Response chan bool
}

func (r ReleaseNode) GetResponseChannelCapacity() int {
	return cap(r.Response)
}

// GetNodesCommand retrieves all nodes from the broker and returns them as copies
type GetNodesCommand struct {
	Response chan []NodeResponse
}

func NewGetNodesCommand() GetNodesCommand {
	return GetNodesCommand{
		Response: make(chan []NodeResponse, 2),
	}
}

func (c GetNodesCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c GetNodesCommand) Execute(b *Broker) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	nodeResponses := make([]NodeResponse, 0, len(b.nodes))
	for _, nodeWithState := range b.nodes {
		// --- Deep copy Node ---
		nodeCopy := nodeWithState.Node // Start with a shallow copy

		// Deep copy Models map
		if nodeWithState.Node.Models != nil {
			nodeCopy.Models = make(map[string]ModelArgs, len(nodeWithState.Node.Models))
			for model, modelArgs := range nodeWithState.Node.Models {
				newArgs := make([]string, len(modelArgs.Args))
				copy(newArgs, modelArgs.Args)
				nodeCopy.Models[model] = ModelArgs{Args: newArgs}
			}
		}

		// Deep copy Hardware slice
		if nodeWithState.Node.Hardware != nil {
			nodeCopy.Hardware = make([]apiconfig.Hardware, len(nodeWithState.Node.Hardware))
			copy(nodeCopy.Hardware, nodeWithState.Node.Hardware)
		}

		// --- Deep copy NodeState ---
		stateCopy := nodeWithState.State // Start with a shallow copy

		// Nil out internal-only fields
		stateCopy.cancelInFlightTask = nil

		// Deep copy pointer fields
		if nodeWithState.State.ReconcileInfo != nil {
			reconcileInfoCopy := *nodeWithState.State.ReconcileInfo
			stateCopy.ReconcileInfo = &reconcileInfoCopy
		}

		if nodeWithState.State.TrainingTask != nil {
			trainingTaskCopy := *nodeWithState.State.TrainingTask // shallow copy of struct

			if nodeWithState.State.TrainingTask.NodeRanks != nil {
				trainingTaskCopy.NodeRanks = make(map[string]int, len(nodeWithState.State.TrainingTask.NodeRanks))
				for k, v := range nodeWithState.State.TrainingTask.NodeRanks {
					trainingTaskCopy.NodeRanks[k] = v
				}
			}
			stateCopy.TrainingTask = &trainingTaskCopy
		}

		nodeResponses = append(nodeResponses, NodeResponse{
			Node:  nodeCopy,
			State: stateCopy,
		})
	}
	logging.Debug("Got nodes", types.Nodes, "size", len(nodeResponses))
	c.Response <- nodeResponses
}

type InferenceResult interface {
	IsSuccess() bool
	GetMessage() string
}

type InferenceSuccess struct {
}

type InferenceError struct {
	Message string
}

func (i InferenceSuccess) IsSuccess() bool {
	return true
}

func (i InferenceSuccess) GetMessage() string {
	return "Success"
}

func (i InferenceError) IsSuccess() bool {
	return false
}

func (i InferenceError) GetMessage() string {
	return i.Message
}

type SyncNodesCommand struct {
	Response chan bool
}

func NewSyncNodesCommand() SyncNodesCommand {
	return SyncNodesCommand{
		Response: make(chan bool, 2),
	}
}

func (s SyncNodesCommand) GetResponseChannelCapacity() int {
	return cap(s.Response)
}

type LockNodesForTrainingCommand struct {
	NodeIds []string
	// FIXME: maybe add description which exact nodes were busy?
	Response chan bool
}

func NewLockNodesForTrainingCommand(nodeIds []string) LockNodesForTrainingCommand {
	return LockNodesForTrainingCommand{
		NodeIds:  nodeIds,
		Response: make(chan bool, 2),
	}
}

func (c LockNodesForTrainingCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

type PocStatus string

const (
	PocStatusIdle       PocStatus = "IDLE"
	PocStatusGenerating PocStatus = "GENERATING"
	PocStatusValidating PocStatus = "VALIDATING"
)

type NodeResult struct {
	Succeeded         bool
	FinalStatus       types.HardwareNodeStatus // The status the node ended up in
	OriginalTarget    types.HardwareNodeStatus // The status it was trying to achieve
	FinalPocStatus    PocStatus
	OriginalPocTarget PocStatus
	Error             string
}

type UpdateNodeResultCommand struct {
	NodeId   string
	Result   NodeResult
	Response chan bool
}

func NewUpdateNodeResultCommand(nodeId string, result NodeResult) UpdateNodeResultCommand {
	return UpdateNodeResultCommand{
		NodeId:   nodeId,
		Result:   result,
		Response: make(chan bool, 2),
	}
}

func (c UpdateNodeResultCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c UpdateNodeResultCommand) Execute(b *Broker) {
	b.mu.Lock()
	defer b.mu.Unlock()

	node, exists := b.nodes[c.NodeId]
	if !exists {
		logging.Warn("Received result for unknown node", types.Nodes, "node_id", c.NodeId)
		c.Response <- false
		return
	}

	// For logging and debugging purposes
	var blockHeight int64
	epochState := b.phaseTracker.GetCurrentEpochState()
	if epochState != nil {
		blockHeight = epochState.CurrentBlock.Height
	} else {
		logging.Warn("UpdateNodeResultCommand: epochState is nil!", types.Nodes, "node_id", c.NodeId)
	}

	// Critical safety check
	if node.State.ReconcileInfo == nil {
		logging.Info("Ignoring stale result for node. node.State.ReconcileInfo is already nil", types.Nodes,
			"node_id", c.NodeId,
			"original_target", c.Result.OriginalTarget,
			"original_poc_target", c.Result.OriginalPocTarget,
			"blockHeight", blockHeight)
		c.Response <- false
		return
	}

	if node.State.ReconcileInfo.Status != c.Result.OriginalTarget ||
		(node.State.ReconcileInfo.Status == types.HardwareNodeStatus_POC && node.State.ReconcileInfo.PocStatus != c.Result.OriginalPocTarget) {
		logging.Info("Ignoring stale result for node", types.Nodes,
			"node_id", c.NodeId,
			"original_target", c.Result.OriginalTarget,
			"original_poc_target", c.Result.OriginalPocTarget,
			"current_reconciling_target", node.State.ReconcileInfo.Status,
			"current_reconciling_poc_target", node.State.ReconcileInfo.PocStatus,
			"blockHeight", blockHeight)
		c.Response <- false
		return
	}

	// Update state
	logging.Info("Finalizing state transition for node", types.Nodes,
		"node_id", c.NodeId,
		"from_status", node.State.CurrentStatus,
		"to_status", c.Result.FinalStatus,
		"from_poc_status", node.State.PocCurrentStatus,
		"to_poc_status", c.Result.FinalPocStatus,
		"succeeded", c.Result.Succeeded,
		"blockHeight", blockHeight)

	node.State.UpdateStatusWithPocStatusNow(c.Result.FinalStatus, c.Result.FinalPocStatus)
	node.State.ReconcileInfo = nil
	node.State.cancelInFlightTask = nil
	if !c.Result.Succeeded {
		node.State.FailureReason = c.Result.Error
	} else {
		// Clear failure reason on success
		node.State.FailureReason = ""
	}

	// Reset POC fields when moving away from POC status
	if c.Result.FinalStatus != types.HardwareNodeStatus_POC {
		node.State.PocIntendedStatus = PocStatusIdle
		node.State.PocCurrentStatus = PocStatusIdle
	}

	c.Response <- true
}
