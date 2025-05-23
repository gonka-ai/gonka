package broker

import (
	"decentralized-api/logging"
	"github.com/productscience/inference/x/inference/types"
)

type StartTrainingCommand struct {
	taskId            uint64
	masterNodeAddress string
	worldSize         int
	nodeRanks         map[string]int
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
	for nodeId, rank := range c.nodeRanks {
		node, nodeFound := broker.nodes[nodeId]
		if !nodeFound || node == nil {
			logging.Error("Node not found or nil", types.Nodes, "node_id", nodeId, "nodeFound", nodeFound, "node == nil", node == nil)
			c.Response <- false
			return
		}

		node.State.IntendedStatus = types.HardwareNodeStatus_TRAINING
		node.State.TrainingTaskId = c.taskId

		client := newNodeClient(&node.Node)

		err := client.Stop()
		if err != nil {
			logging.Error("Error stopping training", types.Nodes, "error", err)
			c.Response <- false
			return
		}

		node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)

		err = client.StartTraining(c.taskId, broker.client.GetAddress(), nodeId, c.masterNodeAddress, rank, c.worldSize)
		if err != nil {
			logging.Error("Error starting training", types.Nodes, "error", err)
			c.Response <- false
			return
		}

		node.State.UpdateStatusNow(types.HardwareNodeStatus_TRAINING)
	}

	c.Response <- true
}
