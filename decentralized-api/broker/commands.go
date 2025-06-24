package broker

import (
	"decentralized-api/logging"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

type Command interface {
	GetResponseChannelCapacity() int
}

type LockAvailableNode struct {
	Model                string
	Version              string
	AcceptEarlierVersion bool
	Response             chan *Node
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

type GetNodesCommand struct {
	Response chan []NodeResponse
}

func (g GetNodesCommand) GetResponseChannelCapacity() int {
	return cap(g.Response)
}

type InferenceResult interface {
	IsSuccess() bool
	GetMessage() string
}

type InferenceSuccess struct {
	Response interface{}
}

type InferenceError struct {
	Message string
	IsFatal bool
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

type SetNodesActualStatusCommand struct {
	StatusUpdates []StatusUpdate
	Response      chan bool
}

type StatusUpdate struct {
	NodeId     string
	PrevStatus types.HardwareNodeStatus
	NewStatus  types.HardwareNodeStatus
	Timestamp  time.Time
}

func (c SetNodesActualStatusCommand) GetResponseChannelCapacity() int {
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
	blockHeight := b.phaseTracker.GetCurrentEpochState().CurrentBlock.Height

	// Critical safety check
	if node.State.ReconcileInfo == nil ||
		node.State.ReconcileInfo.Status != c.Result.OriginalTarget ||
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
	}

	c.Response <- true
}
