package broker

import (
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type StartPocCommand struct {
	Response chan bool
}

func (c StartPocCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c StartPocCommand) Execute(b *Broker) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()

	b.mu.Lock()
	for _, node := range b.nodes {
		// Check if node should be operational based on admin state
		if !node.State.ShouldBeOperational(epochPhaseInfo.Epoch, epochPhaseInfo.Phase) {
			logging.Info("Skipping PoC for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch,
				"current_epoch", epochPhaseInfo,
				"current_phase", epochPhaseInfo.Phase)
			continue
		}

		// Update intended status only
		node.State.IntendedStatus = types.HardwareNodeStatus_POC
	}
	b.mu.Unlock()

	b.TriggerReconciliation()
	logging.Info("StartPocCommand completed, reconciliation triggered", types.PoC)
	c.Response <- true
}

type InitValidateCommand struct {
	Response chan bool
}

func (c InitValidateCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c InitValidateCommand) Execute(b *Broker) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()

	b.mu.Lock()
	for _, node := range b.nodes {
		// Check if node should be operational based on admin state
		if !node.State.ShouldBeOperational(epochPhaseInfo.Epoch, epochPhaseInfo.Phase) {
			logging.Info("Skipping PoC for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch,
				"current_epoch", epochPhaseInfo,
				"current_phase", epochPhaseInfo.Phase)
			continue
		}

		node.State.IntendedStatus = types.HardwareNodeStatus_POC
	}
	b.mu.Unlock()

	b.TriggerReconciliation()
	logging.Info("InitValidateCommand completed, reconciliation triggered for PoC validation", types.PoC)
	c.Response <- true
}

func (c InferenceUpAllCommand) Execute(b *Broker) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()

	b.mu.Lock()
	for _, node := range b.nodes {
		if !node.State.ShouldBeOperational(epochPhaseInfo.Epoch, epochPhaseInfo.Phase) {
			logging.Info("Skipping inference up for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch,
				"current_epoch", epochPhaseInfo,
				"current_phase", epochPhaseInfo.Phase)
			continue
		}
		node.State.IntendedStatus = types.HardwareNodeStatus_INFERENCE
	}
	b.mu.Unlock()

	b.TriggerReconciliation()
	logging.Info("InferenceUpAllCommand completed, reconciliation triggered", types.Nodes)
	c.Response <- true
}
