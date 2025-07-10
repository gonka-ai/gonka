package broker

import (
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type StartTrainingCommand struct {
	taskId            uint64
	masterNodeAddress string
	worldSize         int
	nodeRanks         map[string]int // Key is nodeId
	Response          chan bool
}

func NewStartTrainingCommand(taskId uint64, masterNodeAddress string, worldSize int, nodeRanks map[string]int) StartTrainingCommand {
	return StartTrainingCommand{
		taskId:            taskId,
		masterNodeAddress: masterNodeAddress,
		worldSize:         worldSize,
		nodeRanks:         nodeRanks,
		Response:          make(chan bool, 2),
	}
}

func (c StartTrainingCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c StartTrainingCommand) Execute(broker *Broker) {
	epochState := broker.phaseTracker.GetCurrentEpochState()
	if epochState.IsNilOrNotSynced() {
		logging.Error("StartTrainingCommand executed with nil or not synced epoch state", types.Training,
			"epoch_state", epochState)
		c.Response <- false
		return
	}

	if epochState.CurrentPhase != types.InferencePhase {
		logging.Error("StartTrainingCommand executed in wrong phase", types.Training,
			"current_phase", epochState.CurrentPhase, "expected_phase", types.InferencePhase)
		c.Response <- false
		return
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	for nodeId := range c.nodeRanks {
		node, nodeFound := broker.nodes[nodeId]
		if !nodeFound || node == nil {
			logging.Error("Node not found or nil for training", types.Nodes,
				"node_id", nodeId, "nodeFound", nodeFound, "node == nil", node == nil)
			continue
		}

		if !node.State.ShouldBeOperational(epochState.LatestEpoch.EpochIndex, epochState.CurrentPhase) {
			logging.Error("Selected disabled node for training", types.Nodes,
				"node_id", nodeId,
				"AdminState.Epoch", node.State.AdminState.Epoch,
				"AdminState.Enabled", node.State.AdminState.Enabled,
				"current_epoch", epochState.LatestEpoch.EpochIndex,
				"current_phase", epochState.CurrentPhase)
			continue
		}

		node.State.IntendedStatus = types.HardwareNodeStatus_TRAINING
		node.State.TrainingTask = &TrainingTaskPayload{
			Id:             c.taskId,
			MasterNodeAddr: c.masterNodeAddress,
			NodeRanks:      c.nodeRanks,
			WorldSize:      c.worldSize,
		}
	}

	broker.TriggerReconciliation()

	c.Response <- true
}
