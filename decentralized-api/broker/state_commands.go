package broker

import (
	"decentralized-api/chainphase"
	"decentralized-api/logging"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

type StartPocCommand struct {
	Response chan bool
}

func NewStartPocCommand() StartPocCommand {
	return StartPocCommand{
		Response: make(chan bool, 2),
	}
}

func (c StartPocCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c StartPocCommand) Execute(b *Broker) {
	defer func() {
		logging.Info("StartPocCommand: completed, reconciliation triggered", types.PoC)
		b.TriggerReconciliation()
	}()

	epochState := b.phaseTracker.GetCurrentEpochState()

	if !c.shouldMutateState(b, epochState) {
		logging.Info("StartPocCommand: all nodes already have the desired intended status", types.PoC)
		return
	}

	b.mu.Lock()
	for _, node := range b.nodes {
		// Check if node should be operational based on admin state
		if !node.State.ShouldBeOperational(epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase) {
			logging.Info("Skipping PoC for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch,
				"current_epoch", epochState,
				"current_phase", epochState.CurrentPhase)
			node.State.IntendedStatus = types.HardwareNodeStatus_STOPPED
		} else {
			node.State.IntendedStatus = types.HardwareNodeStatus_POC
			node.State.PocIntendedStatus = PocStatusGenerating
		}
	}
	b.mu.Unlock()

	c.Response <- true
}

func (c StartPocCommand) shouldMutateState(b *Broker, epochState *chainphase.EpochState) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, node := range b.nodes {
		if !node.State.ShouldBeOperational(epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase) &&
			node.State.IntendedStatus != types.HardwareNodeStatus_STOPPED {
			return true
		}

		if node.State.IntendedStatus != types.HardwareNodeStatus_POC ||
			node.State.PocIntendedStatus != PocStatusGenerating {
			return true
		}
	}

	return false
}

type InitValidateCommand struct {
	Response chan bool
}

func NewInitValidateCommand() InitValidateCommand {
	return InitValidateCommand{
		Response: make(chan bool, 2),
	}
}

func (c InitValidateCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c InitValidateCommand) Execute(b *Broker) {
	defer func() {
		logging.Info("InitValidateCommand: completed, reconciliation triggered for PoC validation", types.PoC)
		b.TriggerReconciliation()
	}()

	epochPhaseInfo := b.phaseTracker.GetCurrentEpochState()

	if !c.shouldMutateState(b, epochPhaseInfo) {
		logging.Info("InitValidateCommand: all nodes already have the desired intended status", types.PoC)
		return
	}

	b.mu.Lock()
	for _, node := range b.nodes {
		// Check if node should be operational based on admin state
		if !node.State.ShouldBeOperational(epochPhaseInfo.LatestEpoch.EpochIndex, epochPhaseInfo.CurrentPhase) {
			logging.Info("Skipping PoC for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch,
				"current_epoch", epochPhaseInfo,
				"current_phase", epochPhaseInfo.CurrentPhase)
			node.State.IntendedStatus = types.HardwareNodeStatus_STOPPED
		} else {
			node.State.IntendedStatus = types.HardwareNodeStatus_POC
			node.State.PocIntendedStatus = PocStatusValidating
		}
	}
	b.mu.Unlock()

	c.Response <- true
}

func (c InitValidateCommand) shouldMutateState(b *Broker, epochState *chainphase.EpochState) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, node := range b.nodes {
		if !node.State.ShouldBeOperational(epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase) &&
			node.State.IntendedStatus != types.HardwareNodeStatus_STOPPED {
			return true
		}

		if node.State.IntendedStatus != types.HardwareNodeStatus_POC ||
			node.State.PocIntendedStatus != PocStatusValidating {
			return true
		}
	}

	return false
}

type InferenceUpAllCommand struct {
	Response chan bool
}

func NewInferenceUpAllCommand() InferenceUpAllCommand {
	return InferenceUpAllCommand{
		Response: make(chan bool, 2),
	}
}

func (c InferenceUpAllCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c InferenceUpAllCommand) Execute(b *Broker) {
	defer func() {
		logging.Info("InferenceUpAllCommand: completed, reconciliation triggered", types.Nodes)
		b.TriggerReconciliation()
	}()

	epochState := b.phaseTracker.GetCurrentEpochState()

	if !c.shouldMutateState(b, epochState) {
		logging.Info("InferenceUpAllCommand: all nodes already have the desired intended status", types.Nodes)
		return
	}

	b.mu.Lock()
	for _, node := range b.nodes {
		if !node.State.ShouldBeOperational(epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase) {
			logging.Info("Skipping inference up for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch,
				"current_epoch", epochState,
				"current_phase", epochState.CurrentPhase)
			node.State.IntendedStatus = types.HardwareNodeStatus_STOPPED
		} else if node.State.IntendedStatus == types.HardwareNodeStatus_TRAINING {
			logging.Info("Skipping inference up for node in training state", types.PoC,
				"node_id", node.Node.Id,
				"current_epoch", epochState,
				"current_phase", epochState.CurrentPhase)
			continue
		} else {
			if node.State.IntendedStatus != types.HardwareNodeStatus_INFERENCE {
				logging.Info("Setting node status to Inference", types.PoC,
					"node_id", node.Node.Id,
					"current_epoch", epochState,
					"current_phase", epochState.CurrentPhase,
					"current_intended_status", node.State.IntendedStatus)
			}

			node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
		}
	}
	b.mu.Unlock()

	logging.Info("InferenceUpAllCommand completed, reconciliation triggered", types.Nodes)
	c.Response <- true
}

func (c InferenceUpAllCommand) shouldMutateState(b *Broker, epochState *chainphase.EpochState) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, node := range b.nodes {
		if !node.State.ShouldBeOperational(epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase) &&
			node.State.IntendedStatus != types.HardwareNodeStatus_STOPPED {
			return true
		}

		if node.State.IntendedStatus == types.HardwareNodeStatus_TRAINING {
			continue
		}

		if node.State.IntendedStatus != types.HardwareNodeStatus_INFERENCE {
			return true
		}
	}

	return false
}

type SetNodesActualStatusCommand struct {
	StatusUpdates []StatusUpdate
	Response      chan bool
}

func NewSetNodesActualStatusCommand(statusUpdates []StatusUpdate) SetNodesActualStatusCommand {
	return SetNodesActualStatusCommand{
		StatusUpdates: statusUpdates,
		Response:      make(chan bool, 2),
	}
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

func (c SetNodesActualStatusCommand) Execute(b *Broker) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, update := range c.StatusUpdates {
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

	c.Response <- true
}
