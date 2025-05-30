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
	// First, verify all nodes exist and update their intended status
	nodesToTrain := make([]string, 0, len(c.nodeRanks))
	for nodeId := range c.nodeRanks {
		node, nodeFound := broker.nodes[nodeId]
		if !nodeFound || node == nil {
			logging.Error("Node not found or nil for training", types.Nodes,
				"node_id", nodeId, "nodeFound", nodeFound, "node == nil", node == nil)
			c.Response <- false
			return
		}
		node.State.IntendedStatus = types.HardwareNodeStatus_TRAINING
		node.State.TrainingTaskId = c.taskId
		nodesToTrain = append(nodesToTrain, nodeId)
	}

	// Create a single command instance with common parameters
	// Rank will be set within the command's Execute method based on worker.nodeId
	cmd := StartTrainingNodeCommand{
		TaskId:         c.taskId,
		Participant:    broker.client.GetAddress(),
		MasterNodeAddr: c.masterNodeAddress,
		WorldSize:      c.worldSize,
		NodeRanks:      c.nodeRanks,
	}

	// Execute training start on selected nodes in parallel
	submitted, failed := broker.nodeWorkGroup.ExecuteOnNodes(nodesToTrain, cmd)

	logging.Info("StartTrainingCommand completed", types.Training,
		"submitted", submitted, "failed", failed,
		"requested", len(c.nodeRanks), "task_id", c.taskId)

	// Only report success if all nodes successfully started training
	success := failed == 0 && submitted == len(nodesToTrain)
	c.Response <- success
}
