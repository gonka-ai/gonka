package broker

import (
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type StartPocCommand struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	Response    chan bool
}

func (c StartPocCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c StartPocCommand) Execute(b *Broker) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()
	nodeCmds := make(map[string]NodeWorkerCommand)
	var totalNodes int

	b.mu.Lock()
	totalNodes = len(b.nodes)
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

		// Update intended status first
		node.State.IntendedStatus = types.HardwareNodeStatus_POC

		// Create StartPoCNodeCommand for the worker
		cmd := StartPoCNodeCommand{
			BlockHeight: c.BlockHeight,
			BlockHash:   c.BlockHash,
			PubKey:      c.PubKey,
			CallbackUrl: c.CallbackUrl,
			TotalNodes:  totalNodes,
		}

		nodeCmds[node.Node.Id] = cmd
	}
	b.mu.Unlock()

	submitted, failed := b.nodeWorkGroup.ExecuteOnNodes(nodeCmds)
	logging.Info("StartPocCommand completed", types.PoC,
		"submitted", submitted, "failed", failed, "total", totalNodes)

	c.Response <- true
}

type InitValidateCommand struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	Response    chan bool
}

func (c InitValidateCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c InitValidateCommand) Execute(b *Broker) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()
	nodeCmds := make(map[string]NodeWorkerCommand)
	var totalNodes int

	b.mu.Lock()
	totalNodes = len(b.nodes)
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

		cmd := InitValidateNodeCommand{
			BlockHeight: c.BlockHeight,
			BlockHash:   c.BlockHash,
			PubKey:      c.PubKey,
			CallbackUrl: c.CallbackUrl,
			TotalNodes:  totalNodes,
		}

		nodeCmds[node.Node.Id] = cmd
	}
	b.mu.Unlock()

	// Execute init validate on all nodes in parallel
	submitted, failed := b.nodeWorkGroup.ExecuteOnNodes(nodeCmds)
	logging.Info("InitValidateCommand completed", types.PoC,
		"submitted", submitted, "failed", failed, "total", totalNodes)

	c.Response <- true
}

func (c InferenceUpAllCommand) Execute(b *Broker) {
	epochPhaseInfo := b.phaseTracker.GetCurrentEpochPhaseInfo()
	nodeCmds := make(map[string]NodeWorkerCommand)
	var totalNodes int

	b.mu.Lock()
	totalNodes = len(b.nodes)
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

		nodeCmds[node.Node.Id] = InferenceUpNodeCommand{}
	}
	b.mu.Unlock()

	// Execute inference up on all nodes in parallel
	submitted, failed := b.nodeWorkGroup.ExecuteOnNodes(nodeCmds)

	logging.Info("InferenceUpAllCommand completed", types.Nodes,
		"submitted", submitted, "failed", failed, "total", totalNodes)

	c.Response <- true
}
