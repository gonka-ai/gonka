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
	for _, node := range b.nodes {
		// Check if node should be operational based on admin state
		if !node.State.ShouldBeOperational(epochPhaseInfo.Epoch, epochPhaseInfo.Phase) {
			logging.Info("Skipping PoC for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch)
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
			TotalNodes:  len(b.nodes),
		}

		// Submit to worker
		if worker, exists := b.nodeWorkGroup.GetWorker(node.Node.Id); exists {
			worker.Submit(cmd)
		} else {
			logging.Error("Worker not found for node", types.PoC, "node_id", node.Node.Id)
		}
	}

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
	for _, node := range b.nodes {
		// Check if node should be operational based on admin state
		if !node.State.ShouldBeOperational(epochPhaseInfo.Epoch, epochPhaseInfo.Phase) {
			logging.Info("Skipping PoC for administratively disabled node", types.PoC,
				"node_id", node.Node.Id,
				"admin_enabled", node.State.AdminState.Enabled,
				"admin_epoch", node.State.AdminState.Epoch)
			continue
		}

		node.State.IntendedStatus = types.HardwareNodeStatus_POC

		cmd := InitValidateNodeCommand{
			BlockHeight: c.BlockHeight,
			BlockHash:   c.BlockHash,
			PubKey:      c.PubKey,
			CallbackUrl: c.CallbackUrl,
			TotalNodes:  len(b.nodes),
		}

		// Submit to worker
		if worker, exists := b.nodeWorkGroup.GetWorker(node.Node.Id); exists {
			worker.Submit(cmd)
		} else {
			logging.Error("Worker not found for node", types.PoC, "node_id", node.Node.Id)
		}
	}

	c.Response <- true
}
